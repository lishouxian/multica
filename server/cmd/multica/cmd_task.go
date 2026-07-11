package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Work with the current execution task",
}

var taskOutputCmd = &cobra.Command{
	Use:   "output",
	Short: "Work with structured task output",
}

var taskOutputSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Submit structured output for a playbook step",
	Args:  cobra.NoArgs,
	RunE:  runTaskOutputSet,
}

func runTaskOutputSet(cmd *cobra.Command, _ []string) error {
	taskID, _ := cmd.Flags().GetString("task")
	if taskID == "" {
		taskID = os.Getenv("MULTICA_TASK_ID")
	}
	if taskID == "" {
		return fmt.Errorf("--task is required when MULTICA_TASK_ID is not set")
	}
	path, _ := cmd.Flags().GetString("json-file")
	if path == "" {
		return fmt.Errorf("--json-file is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read output file: %w", err)
	}
	var output any
	if err := json.Unmarshal(raw, &output); err != nil {
		return fmt.Errorf("parse output JSON: %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result map[string]any
	if err := client.PutJSON(ctx, "/api/tasks/"+taskID+"/output", map[string]any{"output": output}, &result); err != nil {
		return fmt.Errorf("submit task output: %w", err)
	}
	outputFormat, _ := cmd.Flags().GetString("output")
	if outputFormat == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Fprintf(os.Stderr, "Structured output accepted for step %s.\n", strVal(result, "step_key"))
	return nil
}

func init() {
	taskOutputSetCmd.Flags().String("task", "", "Task ID (defaults to MULTICA_TASK_ID)")
	taskOutputSetCmd.Flags().String("json-file", "", "Path to the structured output JSON file (required)")
	taskOutputSetCmd.Flags().String("output", "table", "Output format: table or json")
	taskOutputCmd.AddCommand(taskOutputSetCmd)
	taskCmd.AddCommand(taskOutputCmd)
}
