package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var squadCmd = &cobra.Command{
	Use:   "squad",
	Short: "Work with squads",
}

var squadPlaybookCmd = &cobra.Command{Use: "playbook", Short: "Configure a squad's structured handoff playbook"}
var squadRunCmd = &cobra.Command{Use: "run", Short: "Work with squad playbook runs"}

var squadPlaybookGetCmd = &cobra.Command{
	Use: "get <squad-id>", Short: "Get a squad playbook", Args: exactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		var result map[string]any
		if err := client.GetJSON(ctx, "/api/squads/"+args[0]+"/playbook", &result); err != nil {
			return fmt.Errorf("get squad playbook: %w", err)
		}
		return cli.PrintJSON(os.Stdout, result)
	},
}

var squadPlaybookSetCmd = &cobra.Command{
	Use: "set <squad-id>", Short: "Validate and activate a squad playbook from JSON", Args: exactArgs(1), RunE: runSquadPlaybookSet,
}

func runSquadPlaybookSet(cmd *cobra.Command, args []string) error {
	path, _ := cmd.Flags().GetString("file")
	if path == "" {
		return fmt.Errorf("--file is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read playbook: %w", err)
	}
	var definition any
	if err := json.Unmarshal(raw, &definition); err != nil {
		return fmt.Errorf("parse playbook JSON: %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result map[string]any
	if err := client.PutJSON(ctx, "/api/squads/"+args[0]+"/playbook", map[string]any{"definition": definition}, &result); err != nil {
		return fmt.Errorf("save squad playbook: %w", err)
	}
	return cli.PrintJSON(os.Stdout, result)
}

var squadPlaybookDisableCmd = &cobra.Command{
	Use: "disable <squad-id>", Short: "Return a squad to leader-only orchestration", Args: exactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		if err := client.DeleteJSON(ctx, "/api/squads/"+args[0]+"/playbook"); err != nil {
			return fmt.Errorf("disable squad playbook: %w", err)
		}
		fmt.Fprintln(os.Stderr, "Squad playbook disabled.")
		return nil
	},
}

var squadRunStartCmd = &cobra.Command{
	Use: "start <squad-id>", Short: "Start the squad playbook on an assigned root issue", Args: exactArgs(1), RunE: runSquadRunStart,
}

func runSquadRunStart(cmd *cobra.Command, args []string) error {
	issue, _ := cmd.Flags().GetString("issue")
	if issue == "" {
		return fmt.Errorf("--issue is required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	issueRef, err := resolveIssueRef(ctx, client, issue)
	if err != nil {
		return fmt.Errorf("resolve root issue: %w", err)
	}
	body := map[string]any{"root_issue_id": issueRef.ID}
	if path, _ := cmd.Flags().GetString("context-file"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read run context: %w", err)
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("parse run context JSON: %w", err)
		}
		body["context"] = value
	}
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/squads/"+args[0]+"/runs", body, &result); err != nil {
		return fmt.Errorf("start squad run: %w", err)
	}
	return cli.PrintJSON(os.Stdout, result)
}

var squadRunListCmd = &cobra.Command{
	Use: "list <squad-id>", Short: "List recent squad playbook runs", Args: exactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		var result []map[string]any
		if err := client.GetJSON(ctx, "/api/squads/"+args[0]+"/runs", &result); err != nil {
			return fmt.Errorf("list squad runs: %w", err)
		}
		return cli.PrintJSON(os.Stdout, result)
	},
}

var squadRunGetCmd = &cobra.Command{
	Use: "get <run-id>", Short: "Get a squad playbook run and its steps", Args: exactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAPIClient(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		var result map[string]any
		if err := client.GetJSON(ctx, "/api/playbook-runs/"+args[0], &result); err != nil {
			return fmt.Errorf("get squad run: %w", err)
		}
		return cli.PrintJSON(os.Stdout, result)
	},
}

var squadDispatchCmd = &cobra.Command{
	Use: "dispatch <run-id>", Short: "Dispatch or retry a declared playbook step", Args: exactArgs(1), RunE: runSquadDispatch,
}

func runSquadDispatch(cmd *cobra.Command, args []string) error {
	step, _ := cmd.Flags().GetString("step")
	if step == "" {
		return fmt.Errorf("--step is required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	body := map[string]any{"step_key": step}
	if agent, _ := cmd.Flags().GetString("agent"); agent != "" {
		agentID, err := resolveAgent(ctx, client, agent)
		if err != nil {
			return fmt.Errorf("resolve dispatch agent: %w", err)
		}
		body["agent_id"] = agentID
	}
	if path, _ := cmd.Flags().GetString("input-file"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read step input: %w", err)
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("parse step input JSON: %w", err)
		}
		body["input"] = value
	}
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/playbook-runs/"+args[0]+"/dispatch", body, &result); err != nil {
		return fmt.Errorf("dispatch playbook step: %w", err)
	}
	return cli.PrintJSON(os.Stdout, result)
}

// ── List ────────────────────────────────────────────────────────────────────

var squadListCmd = &cobra.Command{
	Use:   "list",
	Short: "List squads in the workspace",
	Args:  cobra.NoArgs,
	RunE:  runSquadList,
}

func runSquadList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var squads []map[string]any
	if err := client.GetJSON(ctx, "/api/squads", &squads); err != nil {
		return fmt.Errorf("list squads: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, squads)
	}

	if len(squads) == 0 {
		fmt.Fprintln(os.Stderr, "No squads found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tLEADER ID\tMEMBERS")
	for _, s := range squads {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			strVal(s, "id"), strVal(s, "name"), strVal(s, "leader_id"),
			memberCountDisplay(s))
	}
	return w.Flush()
}

func memberCountDisplay(m map[string]any) string {
	v, ok := m["member_count"]
	if !ok || v == nil {
		return "-"
	}
	n, ok := v.(float64)
	if !ok || n <= 0 {
		return "-"
	}
	return strconv.Itoa(int(n))
}

// ── Get ─────────────────────────────────────────────────────────────────────

var squadGetCmd = &cobra.Command{
	Use:   "get <squad-id>",
	Short: "Get squad details",
	Args:  exactArgs(1),
	RunE:  runSquadGet,
}

func runSquadGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var squad map[string]any
	if err := client.GetJSON(ctx, "/api/squads/"+args[0], &squad); err != nil {
		return fmt.Errorf("get squad: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, squad)
	}

	fmt.Printf("ID:           %s\n", strVal(squad, "id"))
	fmt.Printf("Name:         %s\n", strVal(squad, "name"))
	fmt.Printf("Description:  %s\n", strVal(squad, "description"))
	fmt.Printf("Leader ID:    %s\n", strVal(squad, "leader_id"))
	fmt.Printf("Created:      %s\n", strVal(squad, "created_at"))
	if inst := strVal(squad, "instructions"); inst != "" {
		fmt.Printf("Instructions: %s\n", inst)
	}
	return nil
}

// ── Create ──────────────────────────────────────────────────────────────────

var squadCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new squad",
	Args:  cobra.NoArgs,
	RunE:  runSquadCreate,
}

func runSquadCreate(cmd *cobra.Command, _ []string) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	leader, _ := cmd.Flags().GetString("leader")
	if leader == "" {
		return fmt.Errorf("--leader is required (agent name or ID)")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	leaderID, err := resolveAgent(ctx, client, leader)
	if err != nil {
		return fmt.Errorf("resolve leader: %w", err)
	}

	body := map[string]any{
		"name":      name,
		"leader_id": leaderID,
	}
	if v, _ := cmd.Flags().GetString("description"); v != "" {
		body["description"] = v
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/squads", body, &result); err != nil {
		return fmt.Errorf("create squad: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Printf("Squad created: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}

// ── Update ──────────────────────────────────────────────────────────────────

var squadUpdateCmd = &cobra.Command{
	Use:   "update <squad-id>",
	Short: "Update a squad",
	Args:  exactArgs(1),
	RunE:  runSquadUpdate,
}

func runSquadUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{}
	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		body["name"] = v
	}
	if cmd.Flags().Changed("description") {
		v, _ := cmd.Flags().GetString("description")
		body["description"] = v
	}
	if cmd.Flags().Changed("instructions") {
		v, _ := cmd.Flags().GetString("instructions")
		body["instructions"] = v
	}
	if cmd.Flags().Changed("leader") {
		v, _ := cmd.Flags().GetString("leader")
		leaderID, err := resolveAgent(ctx, client, v)
		if err != nil {
			return fmt.Errorf("resolve leader: %w", err)
		}
		body["leader_id"] = leaderID
	}
	if cmd.Flags().Changed("avatar-url") {
		v, _ := cmd.Flags().GetString("avatar-url")
		body["avatar_url"] = v
	}

	if len(body) == 0 {
		return fmt.Errorf("no fields to update; use flags like --name, --description, --instructions, --leader")
	}

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/squads/"+args[0], body, &result); err != nil {
		return fmt.Errorf("update squad: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Printf("Squad updated: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}

// ── Delete ──────────────────────────────────────────────────────────────────

var squadDeleteCmd = &cobra.Command{
	Use:   "delete <squad-id>",
	Short: "Delete (archive) a squad",
	Args:  exactArgs(1),
	RunE:  runSquadDelete,
}

func runSquadDelete(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if err := client.DeleteJSON(ctx, "/api/squads/"+args[0]); err != nil {
		return fmt.Errorf("delete squad: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{"id": args[0], "deleted": true})
	}
	fmt.Fprintf(os.Stderr, "Squad %s deleted.\n", args[0])
	return nil
}

// ── Members ─────────────────────────────────────────────────────────────────

var squadMemberCmd = &cobra.Command{
	Use:   "member",
	Short: "Work with squad members",
}

var squadMemberListCmd = &cobra.Command{
	Use:   "list <squad-id>",
	Short: "List members of a squad",
	Args:  exactArgs(1),
	RunE:  runSquadMemberList,
}

func runSquadMemberList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var members []map[string]any
	if err := client.GetJSON(ctx, "/api/squads/"+args[0]+"/members", &members); err != nil {
		return fmt.Errorf("list members: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, members)
	}

	if len(members) == 0 {
		fmt.Fprintln(os.Stderr, "No members found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "MEMBER ID\tTYPE\tROLE")
	for _, m := range members {
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			strVal(m, "member_id"), strVal(m, "member_type"), strVal(m, "role"))
	}
	return w.Flush()
}

// ── Member Add ──────────────────────────────────────────────────────────────

var squadMemberAddCmd = &cobra.Command{
	Use:   "add <squad-id>",
	Short: "Add a member to a squad",
	Args:  exactArgs(1),
	RunE:  runSquadMemberAdd,
}

func runSquadMemberAdd(cmd *cobra.Command, args []string) error {
	memberID, _ := cmd.Flags().GetString("member-id")
	memberType, _ := cmd.Flags().GetString("type")
	role, _ := cmd.Flags().GetString("role")

	if memberID == "" {
		return fmt.Errorf("--member-id is required")
	}
	if memberType != "agent" && memberType != "member" {
		return fmt.Errorf("--type must be 'agent' or 'member'")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{
		"member_type": memberType,
		"member_id":   memberID,
		"role":        role,
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/squads/"+args[0]+"/members", body, &result); err != nil {
		return fmt.Errorf("add member: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Printf("Member %s added to squad.\n", memberID)
	return nil
}

// ── Member Set Role ─────────────────────────────────────────────────────────

var squadMemberSetRoleCmd = &cobra.Command{
	Use:   "set-role <squad-id>",
	Short: "Change a squad member's role",
	Args:  exactArgs(1),
	RunE:  runSquadMemberSetRole,
}

func runSquadMemberSetRole(cmd *cobra.Command, args []string) error {
	memberID, _ := cmd.Flags().GetString("member-id")
	memberType, _ := cmd.Flags().GetString("member-type")
	role, _ := cmd.Flags().GetString("role")

	if memberID == "" {
		return fmt.Errorf("--member-id is required")
	}
	if memberType != "agent" && memberType != "member" {
		return fmt.Errorf("--member-type must be 'agent' or 'member'")
	}
	if role == "" {
		return fmt.Errorf("--role is required")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{
		"member_type": memberType,
		"member_id":   memberID,
		"role":        role,
	}

	var result map[string]any
	if err := client.PatchJSON(ctx, "/api/squads/"+args[0]+"/members/role", body, &result); err != nil {
		return fmt.Errorf("set member role: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Fprintf(os.Stderr, "Member %s role updated to %s.\n", memberID, role)
	return nil
}

// ── Member Remove ───────────────────────────────────────────────────────────

var squadMemberRemoveCmd = &cobra.Command{
	Use:   "remove <squad-id>",
	Short: "Remove a member from a squad",
	Args:  exactArgs(1),
	RunE:  runSquadMemberRemove,
}

func runSquadMemberRemove(cmd *cobra.Command, args []string) error {
	memberID, _ := cmd.Flags().GetString("member-id")
	memberType, _ := cmd.Flags().GetString("type")

	if memberID == "" {
		return fmt.Errorf("--member-id is required")
	}
	if memberType != "agent" && memberType != "member" {
		return fmt.Errorf("--type must be 'agent' or 'member'")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{
		"member_type": memberType,
		"member_id":   memberID,
	}

	if err := client.DeleteJSONWithBody(ctx, "/api/squads/"+args[0]+"/members", body); err != nil {
		return fmt.Errorf("remove member: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, map[string]any{"squad_id": args[0], "member_id": memberID, "removed": true})
	}
	fmt.Fprintf(os.Stderr, "Member %s removed from squad.\n", memberID)
	return nil
}

// ── Activity ────────────────────────────────────────────────────────────────

var squadActivityCmd = &cobra.Command{
	Use:   "activity <issue-id> <outcome>",
	Short: "Record a squad leader evaluation on an issue",
	Long: `Record the squad leader's evaluation decision for an issue.

Outcome must be one of:
  action     — leader delegated or took action
  no_action  — leader evaluated and decided no action needed
  failed     — leader encountered an error

This command is intended to be called by squad leader agents after each
trigger to record their decision in the issue timeline.`,
	Args: exactArgs(2),
	RunE: runSquadActivity,
}

func runSquadActivity(cmd *cobra.Command, args []string) error {
	issueID := args[0]
	outcome := args[1]

	if outcome != "action" && outcome != "no_action" && outcome != "failed" {
		return fmt.Errorf("invalid outcome %q; valid values: action, no_action, failed", outcome)
	}

	reason, _ := cmd.Flags().GetString("reason")

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, issueID)
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	body := map[string]any{
		"outcome": outcome,
		"reason":  reason,
	}
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+issueRef.ID+"/squad-evaluated", body, &result); err != nil {
		return fmt.Errorf("record evaluation: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Squad evaluation recorded: %s (issue %s)\n", outcome, issueRef.Display)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	return nil
}

// ── Init ────────────────────────────────────────────────────────────────────

func init() {
	squadPlaybookSetCmd.Flags().String("file", "", "Path to playbook JSON (required)")
	squadPlaybookGetCmd.Flags().String("output", "json", "Output format: json")
	squadPlaybookSetCmd.Flags().String("output", "json", "Output format: json")
	squadRunStartCmd.Flags().String("issue", "", "Root issue ID or identifier (required)")
	squadRunStartCmd.Flags().String("context-file", "", "Optional JSON object with run context")
	squadRunStartCmd.Flags().String("output", "json", "Output format: json")
	squadRunListCmd.Flags().String("output", "json", "Output format: json")
	squadRunGetCmd.Flags().String("output", "json", "Output format: json")
	squadDispatchCmd.Flags().String("step", "", "Declared step key (required)")
	squadDispatchCmd.Flags().String("agent", "", "Optional squad agent name or ID override")
	squadDispatchCmd.Flags().String("input-file", "", "Optional immutable input JSON override")
	squadDispatchCmd.Flags().String("output", "json", "Output format: json")

	squadPlaybookCmd.AddCommand(squadPlaybookGetCmd, squadPlaybookSetCmd, squadPlaybookDisableCmd)
	squadRunCmd.AddCommand(squadRunStartCmd, squadRunListCmd, squadRunGetCmd)
	// list
	squadListCmd.Flags().String("output", "table", "Output format: table or json")

	// get
	squadGetCmd.Flags().String("output", "table", "Output format: table or json")

	// create
	squadCreateCmd.Flags().String("name", "", "Squad name (required)")
	squadCreateCmd.Flags().String("description", "", "Squad description")
	squadCreateCmd.Flags().String("leader", "", "Leader agent (name or ID) — required")
	squadCreateCmd.Flags().String("output", "json", "Output format: table or json")

	// update
	squadUpdateCmd.Flags().String("name", "", "New name")
	squadUpdateCmd.Flags().String("description", "", "New description")
	squadUpdateCmd.Flags().String("instructions", "", "New instructions")
	squadUpdateCmd.Flags().String("leader", "", "New leader agent (name or ID)")
	squadUpdateCmd.Flags().String("avatar-url", "", "New avatar URL")
	squadUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	// delete
	squadDeleteCmd.Flags().String("output", "table", "Output format: table or json")

	// member list
	squadMemberListCmd.Flags().String("output", "table", "Output format: table or json")

	// member add
	squadMemberAddCmd.Flags().String("member-id", "", "Member or agent ID (required)")
	squadMemberAddCmd.Flags().String("type", "agent", "Member type: agent or member")
	squadMemberAddCmd.Flags().String("role", "member", "Role in the squad")
	squadMemberAddCmd.Flags().String("output", "json", "Output format: table or json")

	// member remove
	squadMemberRemoveCmd.Flags().String("member-id", "", "Member or agent ID (required)")
	squadMemberRemoveCmd.Flags().String("type", "agent", "Member type: agent or member")
	squadMemberRemoveCmd.Flags().String("output", "table", "Output format: table or json")

	// member set-role
	squadMemberSetRoleCmd.Flags().String("member-id", "", "Member or agent ID (required)")
	squadMemberSetRoleCmd.Flags().String("member-type", "agent", "Member type: agent or member")
	squadMemberSetRoleCmd.Flags().String("role", "", "New role in the squad (required)")
	squadMemberSetRoleCmd.Flags().String("output", "json", "Output format: table or json")

	// activity
	squadActivityCmd.Flags().String("reason", "", "Short explanation of the decision")
	squadActivityCmd.Flags().String("output", "table", "Output format: table or json")

	squadMemberCmd.AddCommand(squadMemberListCmd)
	squadMemberCmd.AddCommand(squadMemberAddCmd)
	squadMemberCmd.AddCommand(squadMemberRemoveCmd)
	squadMemberCmd.AddCommand(squadMemberSetRoleCmd)

	squadCmd.AddCommand(squadListCmd)
	squadCmd.AddCommand(squadGetCmd)
	squadCmd.AddCommand(squadCreateCmd)
	squadCmd.AddCommand(squadUpdateCmd)
	squadCmd.AddCommand(squadDeleteCmd)
	squadCmd.AddCommand(squadMemberCmd)
	squadCmd.AddCommand(squadActivityCmd)
	squadCmd.AddCommand(squadPlaybookCmd)
	squadCmd.AddCommand(squadRunCmd)
	squadCmd.AddCommand(squadDispatchCmd)
}
