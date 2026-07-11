export type SquadMemberType = "agent" | "member";

export type SquadActivityOutcome = "action" | "no_action" | "failed";

export interface SquadMemberPreview {
  member_type: SquadMemberType;
  member_id: string;
  role: string;
}

export interface Squad {
  id: string;
  workspace_id: string;
  name: string;
  description: string;
  instructions: string;
  avatar_url: string | null;
  leader_id: string;
  creator_id: string;
  created_at: string;
  updated_at: string;
  archived_at: string | null;
  archived_by: string | null;
  // Optional for compatibility with cached/fixture Squad objects created
  // before playbook orchestration was introduced. API parsing defaults the
  // mode to "leader" and the definition id to null.
  orchestration_mode?: "leader" | "playbook";
  workflow_definition_id?: string | null;
  member_count?: number;
  member_preview?: SquadMemberPreview[];
}

export interface SquadPlaybookDefinition {
  id: string;
  squad_id: string;
  name: string;
  version: number;
  definition: Record<string, unknown>;
  updated_at: string;
}

export interface SquadPlaybookResponse {
  orchestration_mode: "leader" | "playbook";
  definition: SquadPlaybookDefinition | null;
}

export interface PlaybookNodeRun {
  id: string;
  step_key: string;
  agent_id: string;
  issue_id: string | null;
  task_id: string | null;
  accepted_task_id: string | null;
  status: string;
  input_snapshot: Record<string, unknown>;
  output: unknown;
  attempt: number;
  error: string;
  updated_at: string;
}

export interface PlaybookRun {
  id: string;
  workflow_definition_id: string;
  workflow_definition_version: number;
  squad_id: string;
  root_issue_id: string;
  status: string;
  context: Record<string, unknown>;
  created_at: string;
  updated_at: string;
  completed_at: string | null;
  definition_snapshot: Record<string, unknown>;
  nodes: PlaybookNodeRun[];
}

export interface SquadMember {
  id: string;
  squad_id: string;
  member_type: SquadMemberType;
  member_id: string;
  role: string;
  created_at: string;
}

export interface SquadActivityLog {
  id: string;
  squad_id: string;
  issue_id: string;
  trigger_comment_id: string | null;
  leader_id: string;
  outcome: SquadActivityOutcome;
  details: unknown;
  created_at: string;
}

export interface CreateSquadRequest {
  name: string;
  description?: string;
  leader_id: string;
  avatar_url?: string;
}

export interface UpdateSquadRequest {
  name?: string;
  description?: string;
  instructions?: string;
  leader_id?: string;
  avatar_url?: string;
}

export interface AddSquadMemberRequest {
  member_type: SquadMemberType;
  member_id: string;
  role?: string;
}

export interface RemoveSquadMemberRequest {
  member_type: SquadMemberType;
  member_id: string;
}

export interface UpdateSquadMemberRoleRequest {
  member_type: SquadMemberType;
  member_id: string;
  role: string;
}

export interface CreateSquadActivityLogRequest {
  squad_id: string;
  issue_id: string;
  trigger_comment_id?: string;
  outcome: SquadActivityOutcome;
  details?: unknown;
}

// SquadMemberStatus mirrors the five-way bucket the back-end derives in
// handler/squad.go::deriveSquadMemberStatus. Kept as a string union here
// (rather than re-derived from snapshot data) so the squad page can render
// the freshest server-side judgement without re-fetching the agent
// snapshot / runtime list. `archived` wins over every runtime/task signal.
export type SquadMemberStatusValue =
  | "working"
  | "idle"
  | "offline"
  | "unstable"
  | "archived";

export interface SquadActiveIssueBrief {
  issue_id: string;
  identifier: string;
  title: string;
  issue_status: string;
}

export interface SquadMemberStatus {
  member_type: SquadMemberType;
  member_id: string;
  // Human members are returned with status === null so the UI can render
  // them in the same list without showing a status pill (v1 has no
  // presence signal for humans).
  status: SquadMemberStatusValue | null;
  active_issues: SquadActiveIssueBrief[];
  last_active_at: string | null;
}

export interface SquadMemberStatusListResponse {
  members: SquadMemberStatus[];
}
