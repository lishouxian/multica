package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Workflow definitions (feat/workflow-v0).
//
// A definition is a YAML playbook the engine compiles into a staged issue
// tree: steps sharing a stage run in parallel, stages advance in order, and
// the engine — not an LLM — executes every transition. Routing is restricted
// on purpose: enum-equality routes with a mandatory default, a single
// declared back-edge per approval step, and nothing else. Fuzzy decisions
// belong to agent steps that write an enum output; the transition itself
// stays deterministic.

// Step types. An empty type is a work step (agent or member does the work and
// flips the issue to done). Approval steps map terminal status to a verdict:
// done = approved, cancelled = rejected.
const (
	WorkflowStepTypeWork     = ""
	WorkflowStepTypeApproval = "approval"
)

// Routing sentinels usable wherever a step key is expected as a target.
const (
	WorkflowTargetDone           = "done"
	WorkflowTargetNeedsAttention = "needs_attention"
)

const workflowDefaultStepTimeout = 24 * time.Hour

// WorkflowDef is the parsed YAML playbook.
type WorkflowDef struct {
	Name     string            `yaml:"name"`
	Vars     []string          `yaml:"vars"`
	Policies WorkflowPolicies  `yaml:"policies"`
	Steps    []WorkflowStepDef `yaml:"steps"`
}

type WorkflowPolicies struct {
	RunDeadline string `yaml:"run_deadline"` // Go duration string, e.g. "72h"
}

type WorkflowStepDef struct {
	Key      string              `yaml:"key"`
	Stage    int                 `yaml:"stage"`
	Type     string              `yaml:"type"` // "" | "approval"
	Title    string              `yaml:"title"`
	Assignee string              `yaml:"assignee"` // "name" | "agent:name" | "member:name"
	Prompt   string              `yaml:"prompt"`
	Outputs  map[string][]string `yaml:"outputs"` // output key -> allowed enum values ([] = free text)
	Timeout  string              `yaml:"timeout"` // Go duration string; default 24h
	OnFail   *WorkflowOnFail     `yaml:"on_fail"`
	OnReject *WorkflowOnReject   `yaml:"on_reject"`
	Next     *WorkflowNext       `yaml:"next"`
}

type WorkflowOnFail struct {
	Retry int    `yaml:"retry"`
	Goto  string `yaml:"goto"`
}

type WorkflowOnReject struct {
	BackTo   string `yaml:"back_to"`
	MaxLoops int    `yaml:"max_loops"`
}

type WorkflowNext struct {
	// Goto jumps unconditionally; mutually exclusive with When/Routes.
	Goto string `yaml:"goto"`
	// When names an upstream output: "steps.<key>.outputs.<name>".
	When    string            `yaml:"when"`
	Routes  map[string]string `yaml:"routes"`
	Default string            `yaml:"default"`
}

var workflowKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var workflowWhenRe = regexp.MustCompile(`^steps\.([a-z][a-z0-9_]*)\.outputs\.([a-zA-Z0-9_]+)$`)

// ParseWorkflowDef parses and validates YAML source. The returned def is safe
// to hand to the engine: every structural invariant the advance reducer
// relies on has been checked here.
func ParseWorkflowDef(source string) (*WorkflowDef, error) {
	var def WorkflowDef
	dec := yaml.NewDecoder(strings.NewReader(source))
	dec.KnownFields(true)
	if err := dec.Decode(&def); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if err := def.validate(); err != nil {
		return nil, err
	}
	return &def, nil
}

// SHA returns the content hash runs pin in their state.
func WorkflowDefSHA(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:8])
}

func (d *WorkflowDef) validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if len(d.Steps) == 0 {
		return fmt.Errorf("at least one step is required")
	}
	if d.Policies.RunDeadline != "" {
		if _, err := time.ParseDuration(d.Policies.RunDeadline); err != nil {
			return fmt.Errorf("policies.run_deadline: %w", err)
		}
	}

	byKey := map[string]*WorkflowStepDef{}
	prevStage := 0
	for i := range d.Steps {
		s := &d.Steps[i]
		if !workflowKeyRe.MatchString(s.Key) {
			return fmt.Errorf("step %d: key %q must be snake_case", i, s.Key)
		}
		if s.Key == WorkflowTargetDone || s.Key == WorkflowTargetNeedsAttention {
			return fmt.Errorf("step %q: key collides with a routing sentinel", s.Key)
		}
		if _, dup := byKey[s.Key]; dup {
			return fmt.Errorf("duplicate step key %q", s.Key)
		}
		byKey[s.Key] = s
		if s.Stage < 1 {
			return fmt.Errorf("step %q: stage must be >= 1", s.Key)
		}
		if s.Stage < prevStage {
			return fmt.Errorf("step %q: stages must be non-decreasing in file order", s.Key)
		}
		prevStage = s.Stage
		if s.Type != WorkflowStepTypeWork && s.Type != WorkflowStepTypeApproval {
			return fmt.Errorf("step %q: unknown type %q", s.Key, s.Type)
		}
		if strings.TrimSpace(s.Assignee) == "" {
			return fmt.Errorf("step %q: assignee is required", s.Key)
		}
		if s.Timeout != "" {
			if _, err := time.ParseDuration(s.Timeout); err != nil {
				return fmt.Errorf("step %q: timeout: %w", s.Key, err)
			}
		}
		if s.Type == WorkflowStepTypeApproval {
			if s.OnFail != nil {
				return fmt.Errorf("step %q: approval steps use on_reject, not on_fail", s.Key)
			}
			if len(s.Outputs) > 0 {
				return fmt.Errorf("step %q: approval steps have no outputs (verdict comes from Done/Cancel)", s.Key)
			}
		}
		if s.OnReject != nil && s.Type != WorkflowStepTypeApproval {
			return fmt.Errorf("step %q: on_reject is only valid on approval steps", s.Key)
		}
		if s.OnFail != nil {
			if s.OnFail.Retry < 0 {
				return fmt.Errorf("step %q: on_fail.retry must be >= 0", s.Key)
			}
			if s.OnFail.Retry > 0 && s.OnFail.Goto != "" {
				return fmt.Errorf("step %q: on_fail.retry and on_fail.goto are mutually exclusive", s.Key)
			}
		}
	}

	// Per-stage invariants: at most one router per stage, approvals alone in
	// their stage (so a rejection is never ambiguous with parallel work).
	routersPerStage := map[int]int{}
	for i := range d.Steps {
		s := &d.Steps[i]
		if s.Next != nil {
			routersPerStage[s.Stage]++
		}
		if s.Type == WorkflowStepTypeApproval && len(d.StageGroup(s.Stage)) > 1 {
			return fmt.Errorf("step %q: approval steps must be alone in their stage", s.Key)
		}
	}
	for stage, n := range routersPerStage {
		if n > 1 {
			return fmt.Errorf("stage %d: at most one step per stage may declare next", stage)
		}
	}

	// Target and reference checks.
	targetOK := func(t string) bool {
		if t == WorkflowTargetDone || t == WorkflowTargetNeedsAttention {
			return true
		}
		_, ok := byKey[t]
		return ok
	}
	for i := range d.Steps {
		s := &d.Steps[i]
		if s.Next != nil {
			n := s.Next
			if n.Goto != "" && (n.When != "" || len(n.Routes) > 0 || n.Default != "") {
				return fmt.Errorf("step %q: next.goto is mutually exclusive with when/routes/default", s.Key)
			}
			if n.Goto != "" {
				if !targetOK(n.Goto) {
					return fmt.Errorf("step %q: next.goto references unknown step %q", s.Key, n.Goto)
				}
				if tgt, ok := byKey[n.Goto]; ok && tgt.Stage <= s.Stage {
					return fmt.Errorf("step %q: next.goto must target a later stage (use on_reject for back-edges)", s.Key)
				}
			} else {
				m := workflowWhenRe.FindStringSubmatch(n.When)
				if m == nil {
					return fmt.Errorf("step %q: next.when must look like steps.<key>.outputs.<name>", s.Key)
				}
				src, ok := byKey[m[1]]
				if !ok {
					return fmt.Errorf("step %q: next.when references unknown step %q", s.Key, m[1])
				}
				if src.Stage > s.Stage {
					return fmt.Errorf("step %q: next.when must reference the current or an earlier stage", s.Key)
				}
				if _, ok := src.Outputs[m[2]]; !ok {
					return fmt.Errorf("step %q: next.when references undeclared output %q of step %q", s.Key, m[2], m[1])
				}
				if len(n.Routes) == 0 {
					return fmt.Errorf("step %q: next.routes is required with next.when", s.Key)
				}
				if n.Default == "" {
					return fmt.Errorf("step %q: next.default is required (enum drift downgrades, not crashes)", s.Key)
				}
				for v, t := range n.Routes {
					if !targetOK(t) {
						return fmt.Errorf("step %q: route %q targets unknown step %q", s.Key, v, t)
					}
					if tgt, ok := byKey[t]; ok && tgt.Stage <= s.Stage {
						return fmt.Errorf("step %q: route %q must target a later stage (use on_reject for back-edges)", s.Key, v)
					}
					if enum := src.Outputs[m[2]]; len(enum) > 0 && !containsString(enum, v) {
						return fmt.Errorf("step %q: route value %q is not in the declared enum of %s", s.Key, v, n.When)
					}
				}
				if !targetOK(n.Default) {
					return fmt.Errorf("step %q: next.default targets unknown step %q", s.Key, n.Default)
				}
				if tgt, ok := byKey[n.Default]; ok && tgt.Stage <= s.Stage {
					return fmt.Errorf("step %q: next.default must target a later stage", s.Key)
				}
			}
		}
		if s.OnReject != nil {
			tgt, ok := byKey[s.OnReject.BackTo]
			if !ok {
				return fmt.Errorf("step %q: on_reject.back_to references unknown step %q", s.Key, s.OnReject.BackTo)
			}
			if tgt.Stage >= s.Stage {
				return fmt.Errorf("step %q: on_reject.back_to must target an earlier stage", s.Key)
			}
			if s.OnReject.MaxLoops < 1 {
				return fmt.Errorf("step %q: on_reject.max_loops must be >= 1", s.Key)
			}
		}
		if s.OnFail != nil && s.OnFail.Goto != "" {
			if !targetOK(s.OnFail.Goto) {
				return fmt.Errorf("step %q: on_fail.goto references unknown step %q", s.Key, s.OnFail.Goto)
			}
		}
	}
	return nil
}

// Step returns the step with the given key, or nil.
func (d *WorkflowDef) Step(key string) *WorkflowStepDef {
	for i := range d.Steps {
		if d.Steps[i].Key == key {
			return &d.Steps[i]
		}
	}
	return nil
}

// StageGroup returns every step sharing the given stage, in file order.
func (d *WorkflowDef) StageGroup(stage int) []*WorkflowStepDef {
	var out []*WorkflowStepDef
	for i := range d.Steps {
		if d.Steps[i].Stage == stage {
			out = append(out, &d.Steps[i])
		}
	}
	return out
}

// Stages returns the distinct stage ordinals in ascending order.
func (d *WorkflowDef) Stages() []int {
	seen := map[int]bool{}
	var out []int
	for i := range d.Steps {
		if !seen[d.Steps[i].Stage] {
			seen[d.Steps[i].Stage] = true
			out = append(out, d.Steps[i].Stage)
		}
	}
	sort.Ints(out)
	return out
}

// NextStageAfter returns the smallest stage ordinal greater than the given
// one, or 0 when none exists (end of the playbook).
func (d *WorkflowDef) NextStageAfter(stage int) int {
	best := 0
	for _, s := range d.Stages() {
		if s > stage && (best == 0 || s < best) {
			best = s
		}
	}
	return best
}

// StepTimeout returns the effective timeout for a step.
func (s *WorkflowStepDef) StepTimeout() time.Duration {
	if s.Timeout == "" {
		return workflowDefaultStepTimeout
	}
	dur, err := time.ParseDuration(s.Timeout)
	if err != nil {
		return workflowDefaultStepTimeout
	}
	return dur
}

var workflowInterpRe = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.]+)\s*\}\}`)

// InterpolateWorkflowTemplate substitutes {{vars.x}}, {{steps.k.outputs.o}},
// {{steps.k.outcome}}, {{steps.k.reject_reason}} and {{run.root_issue}}.
// Unknown references render as-is so a typo is visible in the issue body
// instead of silently disappearing.
func InterpolateWorkflowTemplate(tpl string, scope map[string]string) string {
	return workflowInterpRe.ReplaceAllStringFunc(tpl, func(m string) string {
		ref := strings.TrimSpace(m[2 : len(m)-2])
		if v, ok := scope[ref]; ok {
			return v
		}
		return m
	})
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
