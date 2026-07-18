package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// `multica workflow` (feat/workflow-v0): push YAML playbooks, start runs,
// and control in-flight runs. Definitions live in git and are pushed here;
// run state lives on the run root issue and is fully inspectable via
// `multica workflow status`.

var workflowCmd = &cobra.Command{
	Use:   "workflow",
	Short: "Manage workflow playbooks and runs",
	Long: `Workflows compile a YAML playbook into a staged issue tree.
Steps sharing a stage run in parallel; the engine promotes stages, routes on
step outputs, loops back on rejected approvals, and escalates failures to a
human. Definitions are pushed from YAML files kept in git.`,
}

var workflowValidateCmd = &cobra.Command{
	Use:   "validate <file.yaml>",
	Short: "Validate a workflow YAML file without saving it",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkflowValidate,
}

var workflowPushCmd = &cobra.Command{
	Use:   "push <file.yaml>",
	Short: "Validate and upsert a workflow definition (keyed by its YAML name)",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkflowPush,
}

var workflowListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workflow definitions",
	RunE:  runWorkflowList,
}

var workflowRunCmd = &cobra.Command{
	Use:   "run <name-or-id>",
	Short: "Start a run of a workflow",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkflowRun,
}

var workflowRunsCmd = &cobra.Command{
	Use:   "runs",
	Short: "List workflow runs",
	RunE:  runWorkflowRuns,
}

var workflowStatusCmd = &cobra.Command{
	Use:   "status <run-root-issue-id>",
	Short: "Show a run's state, steps and attempts",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkflowStatus,
}

func workflowControlCmd(use, short, action string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <run-root-issue-id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowControl(cmd, args[0], action, nil)
		},
	}
}

var workflowRetryCmd = &cobra.Command{
	Use:   "retry <run-root-issue-id>",
	Short: "Materialize a fresh attempt for one step and resume the run",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		step, _ := cmd.Flags().GetString("step")
		if step == "" {
			return fmt.Errorf("--step is required")
		}
		return runWorkflowControl(cmd, args[0], "retry", map[string]any{"step": step})
	},
}

func init() {
	workflowCmd.AddCommand(workflowValidateCmd)
	workflowCmd.AddCommand(workflowPushCmd)
	workflowCmd.AddCommand(workflowListCmd)
	workflowCmd.AddCommand(workflowRunCmd)
	workflowCmd.AddCommand(workflowRunsCmd)
	workflowCmd.AddCommand(workflowStatusCmd)
	workflowCmd.AddCommand(workflowControlCmd("pause", "Pause a run (in-flight steps finish; nothing new starts)", "pause"))
	workflowCmd.AddCommand(workflowControlCmd("resume", "Resume a paused or needs-attention run", "resume"))
	workflowCmd.AddCommand(workflowControlCmd("cancel", "Cancel a run and its open step issues", "cancel"))
	workflowCmd.AddCommand(workflowControlCmd("eject", "Detach the engine; the issue tree becomes fully manual", "eject"))
	workflowCmd.AddCommand(workflowRetryCmd)

	workflowListCmd.Flags().String("output", "table", "Output format: table or json")
	workflowRunsCmd.Flags().String("output", "table", "Output format: table or json")
	workflowRunCmd.Flags().StringSlice("var", nil, "Run variable as key=value (repeatable)")
	workflowStatusCmd.Flags().String("output", "table", "Output format: table or json")
	workflowRetryCmd.Flags().String("step", "", "Step key to retry (required)")
}

func runWorkflowValidate(cmd *cobra.Command, args []string) error {
	source, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var resp struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
		Name  string `json:"name"`
		Steps []struct {
			Key   string `json:"key"`
			Stage int    `json:"stage"`
		} `json:"steps"`
	}
	if err := client.PostJSON(ctx, "/api/workflows/validate", map[string]string{"source": string(source)}, &resp); err != nil {
		return fmt.Errorf("validate workflow: %w", err)
	}
	if !resp.Valid {
		return fmt.Errorf("invalid workflow: %s", resp.Error)
	}
	fmt.Fprintf(os.Stdout, "✓ %s is valid (%s, %d steps)\n", args[0], resp.Name, len(resp.Steps))
	return nil
}

func runWorkflowPush(cmd *cobra.Command, args []string) error {
	source, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var resp map[string]any
	if err := client.PostJSON(ctx, "/api/workflows", map[string]string{"source": string(source)}, &resp); err != nil {
		return fmt.Errorf("push workflow: %w", err)
	}
	fmt.Fprintf(os.Stdout, "✓ pushed workflow %q (%s)\n", strVal(resp, "name"), strVal(resp, "id"))
	return nil
}

func runWorkflowList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var resp struct {
		Workflows []map[string]any `json:"workflows"`
	}
	if err := client.GetJSON(ctx, "/api/workflows", &resp); err != nil {
		return fmt.Errorf("list workflows: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	headers := []string{"ID", "NAME", "STEPS", "UPDATED"}
	rows := make([][]string, 0, len(resp.Workflows))
	for _, wf := range resp.Workflows {
		steps, _ := wf["steps"].([]any)
		rows = append(rows, []string{
			displayID(strVal(wf, "id"), false),
			strVal(wf, "name"),
			fmt.Sprintf("%d", len(steps)),
			strVal(wf, "updated_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// resolveWorkflowRef accepts a definition UUID or a definition name.
func resolveWorkflowRef(ctx context.Context, client *cli.APIClient, ref string) (string, error) {
	if uuidRegexp.MatchString(strings.TrimSpace(ref)) {
		return strings.TrimSpace(ref), nil
	}
	var resp struct {
		Workflows []map[string]any `json:"workflows"`
	}
	if err := client.GetJSON(ctx, "/api/workflows", &resp); err != nil {
		return "", err
	}
	for _, wf := range resp.Workflows {
		if strings.EqualFold(strVal(wf, "name"), ref) {
			return strVal(wf, "id"), nil
		}
	}
	return "", fmt.Errorf("no workflow named %q", ref)
}

func runWorkflowRun(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	id, err := resolveWorkflowRef(ctx, client, args[0])
	if err != nil {
		return err
	}
	varsFlag, _ := cmd.Flags().GetStringSlice("var")
	vars := map[string]string{}
	for _, kv := range varsFlag {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("--var must be key=value, got %q", kv)
		}
		vars[k] = v
	}
	var resp map[string]any
	if err := client.PostJSON(ctx, "/api/workflows/"+id+"/run", map[string]any{"vars": vars}, &resp); err != nil {
		return fmt.Errorf("start run: %w", err)
	}
	fmt.Fprintf(os.Stdout, "▶ run started: %s (%s) root issue %s\n",
		strVal(resp, "workflow"), strVal(resp, "root_identifier"), strVal(resp, "root_issue_id"))
	return nil
}

func runWorkflowRuns(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var resp struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := client.GetJSON(ctx, "/api/workflows/runs", &resp); err != nil {
		return fmt.Errorf("list runs: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	headers := []string{"ROOT", "WORKFLOW", "STATUS", "FRONTIER", "STARTED"}
	rows := make([][]string, 0, len(resp.Runs))
	for _, run := range resp.Runs {
		frontier, _ := run["frontier"].([]any)
		fs := make([]string, 0, len(frontier))
		for _, f := range frontier {
			if s, ok := f.(string); ok {
				fs = append(fs, s)
			}
		}
		rows = append(rows, []string{
			strVal(run, "root_identifier"),
			strVal(run, "workflow"),
			strVal(run, "status"),
			strings.Join(fs, ","),
			strVal(run, "started_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runWorkflowStatus(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	rootRes, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return err
	}
	var resp map[string]any
	if err := client.GetJSON(ctx, "/api/workflows/runs/"+rootRes.ID, &resp); err != nil {
		return fmt.Errorf("get run: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	fmt.Fprintf(os.Stdout, "%s · %s · %s\n", strVal(resp, "workflow"), strVal(resp, "root_identifier"), strVal(resp, "status"))
	if reason := strVal(resp, "status_reason"); reason != "" {
		fmt.Fprintf(os.Stdout, "  reason: %s\n", reason)
	}
	steps, _ := resp["steps"].([]any)
	headers := []string{"STAGE", "STEP", "TYPE", "ASSIGNEE", "ACTIVE", "LATEST", "STATUS"}
	rows := make([][]string, 0, len(steps))
	for _, s := range steps {
		step, ok := s.(map[string]any)
		if !ok {
			continue
		}
		latest, latestStatus := "—", "pending"
		if attempts, ok := step["attempts"].([]any); ok && len(attempts) > 0 {
			if a, ok := attempts[len(attempts)-1].(map[string]any); ok {
				latest = strVal(a, "identifier")
				latestStatus = strVal(a, "issue_status")
			}
		}
		active := ""
		if b, ok := step["active"].(bool); ok && b {
			active = "●"
		}
		stepType := strVal(step, "type")
		if stepType == "" {
			stepType = "work"
		}
		rows = append(rows, []string{
			fmt.Sprintf("%v", step["stage"]),
			strVal(step, "key"),
			stepType,
			strVal(step, "assignee"),
			active,
			latest,
			latestStatus,
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runWorkflowControl(cmd *cobra.Command, rootRef, action string, body map[string]any) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	rootRes, err := resolveIssueRef(ctx, client, rootRef)
	if err != nil {
		return err
	}
	if body == nil {
		body = map[string]any{}
	}
	var resp map[string]any
	if err := client.PostJSON(ctx, "/api/workflows/runs/"+rootRes.ID+"/"+action, body, &resp); err != nil {
		return fmt.Errorf("%s run: %w", action, err)
	}
	fmt.Fprintf(os.Stdout, "✓ %s: run %s is now %s\n", action, strVal(resp, "root_identifier"), strVal(resp, "status"))
	return nil
}
