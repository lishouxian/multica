package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunTaskOutputSetSubmitsJSONForCurrentTask(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_TASK_ID", "task-123")
	t.Setenv("MULTICA_AGENT_ID", "agent-123")
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-123")

	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/tasks/task-123/output" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"step_key": "triage", "status": "accepted"})
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)

	path := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(path, []byte(`{"outcome":"fix"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("task", "", "")
	cmd.Flags().String("json-file", "", "")
	cmd.Flags().String("output", "table", "")
	_ = cmd.Flags().Set("json-file", path)
	if err := runTaskOutputSet(cmd, nil); err != nil {
		t.Fatalf("runTaskOutputSet: %v", err)
	}
	output, ok := got["output"].(map[string]any)
	if !ok || output["outcome"] != "fix" {
		t.Fatalf("output body = %#v", got)
	}
}
