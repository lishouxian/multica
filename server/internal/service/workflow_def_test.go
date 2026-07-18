package service

import (
	"strings"
	"testing"
)

const validWorkflowYAML = `
name: release-flow
vars: [version]
policies:
  run_deadline: 72h
steps:
  - key: regression
    stage: 1
    assignee: "qa-agent"
    prompt: "Run regression for {{vars.version}}."
    outputs:
      result: [pass, fail]
    timeout: 4h
    on_fail: { retry: 1 }
    next:
      when: "steps.regression.outputs.result"
      routes: { pass: gate }
      default: needs_attention
  - key: changelog
    stage: 1
    assignee: "doc-agent"
    prompt: "Assemble the changelog for {{vars.version}}."
  - key: gate
    stage: 2
    type: approval
    assignee: "member:xian"
    title: "Release approval {{vars.version}}"
    on_reject: { back_to: regression, max_loops: 2 }
  - key: ship
    stage: 3
    assignee: "ops-agent"
    prompt: "Ship {{vars.version}}. Regression: {{steps.regression.outputs.result}}."
`

func TestParseWorkflowDefValid(t *testing.T) {
	def, err := ParseWorkflowDef(validWorkflowYAML)
	if err != nil {
		t.Fatalf("expected valid definition, got %v", err)
	}
	if def.Name != "release-flow" {
		t.Fatalf("name = %q", def.Name)
	}
	if got := def.Stages(); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("stages = %v", got)
	}
	if got := stageKeys(def, 1); len(got) != 2 || got[0] != "regression" || got[1] != "changelog" {
		t.Fatalf("stage 1 keys = %v", got)
	}
	if def.NextStageAfter(2) != 3 {
		t.Fatalf("NextStageAfter(2) = %d", def.NextStageAfter(2))
	}
	if def.NextStageAfter(3) != 0 {
		t.Fatalf("NextStageAfter(3) = %d", def.NextStageAfter(3))
	}
	if def.Step("gate").StepTimeout() != workflowDefaultStepTimeout {
		t.Fatalf("gate timeout should default")
	}
}

func TestParseWorkflowDefRejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(string) string
		wantErr string
	}{
		{"duplicate key", func(s string) string {
			return strings.Replace(s, "key: changelog", "key: regression", 1)
		}, "duplicate step key"},
		{"missing default", func(s string) string {
			return strings.Replace(s, "      default: needs_attention\n", "", 1)
		}, "next.default is required"},
		{"route to earlier stage", func(s string) string {
			return strings.Replace(s, "routes: { pass: gate }", "routes: { pass: regression }", 1)
		}, "must target a later stage"},
		{"back_to later stage", func(s string) string {
			return strings.Replace(s, "back_to: regression", "back_to: ship", 1)
		}, "must target an earlier stage"},
		{"unknown route target", func(s string) string {
			return strings.Replace(s, "routes: { pass: gate }", "routes: { pass: nonexistent }", 1)
		}, "unknown step"},
		{"route value outside enum", func(s string) string {
			return strings.Replace(s, "routes: { pass: gate }", "routes: { maybe: gate }", 1)
		}, "not in the declared enum"},
		{"approval with outputs", func(s string) string {
			return strings.Replace(s, "    type: approval\n", "    type: approval\n    outputs:\n      x: []\n", 1)
		}, "approval steps have no outputs"},
		{"on_reject on work step", func(s string) string {
			return strings.Replace(s, "    on_fail: { retry: 1 }\n", "    on_reject: { back_to: changelog, max_loops: 1 }\n", 1)
		}, "only valid on approval steps"},
		{"decreasing stages", func(s string) string {
			return strings.Replace(s, "  - key: ship\n    stage: 3", "  - key: ship\n    stage: 1", 1)
		}, "non-decreasing"},
		{"unknown field", func(s string) string {
			return strings.Replace(s, "name: release-flow", "name: release-flow\nbogus_field: 1", 1)
		}, "yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWorkflowDef(tc.mutate(validWorkflowYAML))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestApprovalMustBeAloneInStage(t *testing.T) {
	src := strings.Replace(validWorkflowYAML, "  - key: gate\n    stage: 2", "  - key: gate\n    stage: 1", 1)
	// Also drop the routing that now targets an equal stage so we hit the
	// approval-specific error deterministically.
	src = strings.Replace(src, "    next:\n      when: \"steps.regression.outputs.result\"\n      routes: { pass: gate }\n      default: needs_attention\n", "", 1)
	_, err := ParseWorkflowDef(src)
	if err == nil || !strings.Contains(err.Error(), "alone in their stage") {
		t.Fatalf("expected approval-alone error, got %v", err)
	}
}

func TestInterpolateWorkflowTemplate(t *testing.T) {
	scope := map[string]string{
		"vars.version":                    "1.2",
		"steps.regression.outputs.result": "pass",
	}
	got := InterpolateWorkflowTemplate("Ship {{vars.version}} ({{ steps.regression.outputs.result }}) {{unknown.ref}}", scope)
	want := "Ship 1.2 (pass) {{unknown.ref}}"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
