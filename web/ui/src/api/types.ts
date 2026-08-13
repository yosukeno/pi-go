// Mirror of web/wire.go. That file is the source of truth; when the two
// disagree, it wins.

// The last two are the ones a child never has, so a delegated run's own tool
// calls only ever name the first seven: a subagent cannot delegate further, and
// it keeps no task list — its progress is already on the parent's subagent card,
// and it never lives long enough to reach a compaction boundary.
export type ToolName =
  | "read"
  | "ls"
  | "find"
  | "grep"
  | "write"
  | "edit"
  | "bash"
  | "subagent"
  | "todo";

export type Block =
  | { type: "text"; text: string }
  | { type: "thinking"; text: string }
  | { type: "tool_use"; id: string; name: ToolName; input?: unknown }
  // tool_result never appears in messages: the server keys results out into a
  // separate table so replayed history and live rendering have one shape.
  | { type: "tool_result"; tool_use_id: string; text?: string; is_error?: boolean };

export interface Message {
  id: string;
  role: "user" | "assistant";
  content: Block[];
  /** When the message was recorded, epoch ms. Absent on very old transcripts. */
  ts?: number;
}

export interface ReadDetails {
  path: string;
  offset?: number;
  limit?: number;
  total_lines: number;
  shown_lines: number;
  first_line: number;
  truncated?: boolean;
  truncated_by?: string;
}

export interface LsDetails {
  path: string;
  entries: number;
  dirs?: number;
  files?: number;
  total?: number;
  truncated?: boolean;
  entry_limited?: boolean;
}

export interface WriteDetails {
  path: string;
  bytes: number;
  created: boolean;
  diff?: string;
  patch?: string;
  added?: number;
  removed?: number;
}

export interface EditDetails {
  path: string;
  edits: number;
  diff: string;
  patch: string;
  first_changed_line?: number;
  added: number;
  removed: number;
}

export interface BashDetails {
  command: string;
  exit_code: number;
  duration_ms: number;
  timed_out?: boolean;
  truncated?: boolean;
  total_lines?: number;
  full_output_path?: string;
}

export interface FindDetails {
  pattern: string;
  path?: string;
  matches: number;
  // scanned is how many entries were visited. "No matches" after 3 entries and
  // after 200,000 mean different things.
  scanned: number;
  truncated?: boolean;
  limit_hit?: boolean;
}

export interface GrepDetails {
  pattern: string;
  path?: string;
  include?: string;
  matches: number;
  // files is how many distinct files contained a match.
  files: number;
  scanned: number;
  skipped_binary?: number;
  truncated?: boolean;
  limit_hit?: boolean;
}

export type ToolDetails =
  | ReadDetails
  | LsDetails
  | FindDetails
  | GrepDetails
  | WriteDetails
  | EditDetails
  | BashDetails
  | SubagentDetails
  | TodoDetails;

/** TodoStatus mirrors the enum in tools/todo.go. */
export type TodoStatus = "pending" | "in_progress" | "completed" | "cancelled" | "blocked";

export interface TodoItem {
  task: string;
  status: TodoStatus;
}

/**
 * The task list as the agent last wrote it.
 *
 * No counts, unlike LsDetails: everything is in the array, so a precomputed total
 * would only be a second thing able to disagree with it.
 *
 * Every one of these on the timeline is a historical write, and only the newest is
 * the current plan — see TimelineCall.superseded.
 */
export interface TodoDetails {
  todos: TodoItem[];
}

/**
 * What a finished delegation reports. The worktree path is deliberately absent from
 * what the model is told and present here, because a person may need to look at one
 * that was left behind; `session` is the child's transcript, which is where the work
 * that never entered this conversation actually is.
 */
export interface SubagentDetails {
  id: string;
  mode: string;
  model?: string;
  ref?: string;
  commit?: string;
  session?: string;
  worktree?: string;
  turns?: number;
  input_tokens?: number;
  output_tokens?: number;
  exit_code?: number;
  tampered?: boolean;
}

export interface ToolResult {
  call_id: string;
  name?: ToolName;
  text: string;
  is_error?: boolean;
  // Details never enter the model's context, which is why a full diff can live
  // here while the model only sees the truncated text.
  details?: ToolDetails;
  /**
   * A delegated run's process frames, retained client-side when a subagent call
   * settles. The server never sends this: unlike live output, whose bytes are
   * the result text, frames are the run's process and the result is its answer,
   * so keeping both past tool_end renders nothing twice. A transcript replayed
   * from disk has none — the live client is the only place they survive.
   */
  frames?: SubagentFrame[];
}

export interface PendingTool {
  call_id: string;
  name: ToolName;
  args?: unknown;
  started_at: number;
  /**
   * What the call has printed so far, for the tools that report it (bash only).
   * Accumulated server-side as well, because tool_partial is not replayed: a page
   * that connects mid-command sees this in the snapshot or sees nothing.
   */
  output?: string;
  /**
   * The structured progress events of a call that reports them — today only a
   * subagent, whose child emits this same event contract one level down. Kept
   * server-side too, for the same reason `output` is.
   */
  frames?: SubagentFrame[];
}

/**
 * The raw arguments of a call the model is still generating. A `write` with a
 * huge `content` streams for minutes before its tool_start, and this is the
 * only sign of life in that gap. `head`/`tail` are windows onto the raw,
 * still JSON-escaped argument text — never parsed, because it is incomplete
 * by definition until tool_start replaces the entry. Both are capped
 * server-side (head 4096, tail 8192) and `bytes` is the authoritative total,
 * so a reconnecting client can rebuild the entry from the snapshot alone.
 */
export interface IncomingArgs {
  call_id: string;
  /** Empty until the first fragment naming the tool arrives. */
  name: string;
  head?: string;
  tail?: string;
  bytes: number;
  /** Content newlines seen so far — the live +N of a write in flight. */
  lines?: number;
  ts?: number;
}

/**
 * One event from a delegated run. It is the same shape as the events of this run,
 * one level down, which is what lets a subagent be rendered without a second
 * vocabulary. Every field is optional because which ones are set depends on `type`.
 */
/**
 * The frame kinds the parent forwards, per frameWorthForwarding in tools/subagent.go.
 *
 * Its own union rather than EventType: a child runs as `pi-go -mode json`, so its
 * events come from the CLI contract, which has kinds the browser's stream does not —
 * `session` is one, and typing frames as EventType made it a compile error to
 * describe a real frame.
 *
 * Left open to other strings on purpose. The allowlist is enforced in Go, and if it
 * ever grows, a closed union here would be a lie while the runtime quietly ignores
 * the new kind — which is the behaviour we want.
 */
export type SubagentFrameType =
  | "session"
  | "turn_start"
  | "tool_start"
  | "tool_end"
  | "run_end"
  // eslint-disable-next-line @typescript-eslint/ban-types
  | (string & {});

export interface SubagentFrame {
  type: SubagentFrameType;
  turn?: number;
  session?: string;
  /**
   * The model the child is running, on the session header — the first frame a
   * delegation produces, so a card can say what it is running on from the start
   * rather than only once the settled details repeat it.
   */
  model?: string;
  /**
   * Ties a delegated tool_start to its tool_end. Needed rather than convenient: a
   * child runs calls in parallel, so one turn can hold three greps, and pairing them
   * by name would attribute the results in whatever order they finished.
   */
  call_id?: string;
  name?: ToolName | string;
  /**
   * The call's arguments on tool_start — the parent's wire contract puts them
   * there, and the child is forwarded verbatim, so a row can say which file a
   * read read rather than just "read".
   */
  args?: unknown;
  text?: string;
  is_error?: boolean;
  /**
   * The call's structured payload on tool_end (ReadDetails and friends), the
   * same shape the parent's own tool results carry: the child speaks this same
   * contract one level down, so the result components render it unchanged.
   */
  details?: ToolDetails;
  error?: string;
  stop_reason?: string;
}

export interface PendingGate {
  gate_id: string;
  call_id: string;
  tool: ToolName;
  args?: unknown;
  // Absolute epoch milliseconds. Never a duration: a reloaded page has to be
  // able to recompute the time left.
  deadline: number;
  danger?: string[];
}

export interface Live {
  run_id?: string;
  active: boolean;
  turn?: number;
  message_id?: string;
  thinking?: string;
  text?: string;
  pending_tools: PendingTool[];
  pending_gates: PendingGate[];
  /**
   * Calls whose arguments are still streaming in, oldest first. Every entry
   * precedes its tool_start, which removes it: the pending-tool card takes
   * over from there.
   */
  incoming?: IncomingArgs[];
}

export interface RunInfo {
  active: boolean;
  run_id?: string;
  model?: string;
  provider?: string;
  /** From the model catalog; absent for a model that is not in it. */
  context_window?: number;
}

export type PolicyMode = "strict" | "standard" | "auto";

export interface PolicyState {
  mode: PolicyMode;
  remaining_turns?: number;
}

export interface Usage {
  input: number;
  output: number;
  cache_read?: number;
  reasoning?: number;
}

export interface Snapshot {
  seq: number;
  messages: Message[];
  results: Record<string, ToolResult>;
  live: Live;
  run: RunInfo;
  policy: PolicyState;
  /** Running total, for cost. */
  usage: Usage;
  /**
   * The latest turn's prompt size, for occupancy. Not derivable from `usage`:
   * that accumulates across turns and would read many times the window.
   */
  context_tokens: number;
  /**
   * Estimated fixed request cost (system prompt + tool schemas), for the
   * context meter's breakdown. An estimate, never a measurement.
   */
  overhead_tokens?: number;
  /**
   * Prompt size at which the server starts dropping old tool results; absent or 0
   * when clearing is off. The gauge colours its bands against this rather than
   * against fractions of the window, because clearing holds occupancy just below it.
   */
  clear_trigger?: number;
}

export type EventType =
  | "snapshot"
  | "run_start"
  | "user_message"
  | "turn_start"
  | "thinking"
  | "token"
  | "message"
  | "tool_args"
  | "tool_start"
  | "tool_partial"
  | "tool_end"
  | "retry"
  | "run_end"
  | "error"
  | "gate_request"
  | "gate_deadline"
  | "gate_resolved"
  | "gate_auto"
  | "policy_changed"
  | "policy_reverted"
  | "model_changed"
  | "rewound"
  | "compacted";

export interface AgentEvent {
  seq: number;
  type: EventType;
  ts: number;

  run_id?: string;
  model?: string;
  provider?: string;
  context_window?: number;
  turn?: number;

  message_id?: string;
  role?: "user" | "assistant";
  text?: string;
  content?: Block[];

  call_id?: string;
  name?: ToolName;
  args?: unknown;
  is_error?: boolean;
  details?: ToolDetails;
  /** One event from a delegated run, on `tool_partial`. See SubagentFrame. */
  frame?: SubagentFrame;

  gate_id?: string;
  deadline?: number;
  danger?: string[];
  allow?: boolean;
  reason?: string;
  by?: string;
  rule?: string;

  policy?: PolicyState;
  from?: string;
  to?: string;

  attempt?: number;
  max?: number;
  delay_ms?: number;

  stop_reason?: string;
  usage?: Usage;
  error?: string;
  // Steering messages the run accepted but never passed to the model.
  undelivered?: string[];

  snapshot?: Snapshot;
}

export interface SessionInfo {
  path: string;
  id: string;
  title: string;
  /** Pinned sessions sort above the rest; set from the sidebar's ⋮ menu. */
  pinned?: boolean;
  cwd?: string;
  model?: string;
  messages: number;
  created?: number;
  updated?: number;
}

export interface SkillInfo {
  name: string;
  description: string;
  // The absolute path to SKILL.md. It is already in the model's system prompt, so
  // showing it here keeps the user from knowing less than the model does.
  path: string;
  dir: string;
  source: string;
  manual_only?: boolean;
}

// StarterCard is one card on the empty conversation. The content comes from a
// skill's starters.json, so the deployment decides what its agent is for; the
// server has already validated that exactly one action is present, that the icon
// is a known name, and that a panel card names a registered panel.
export interface StarterCard {
  icon?: string;
  title: string;
  label?: string;
  /** Put this text in the composer. Mutually exclusive with panel. */
  prompt?: string;
  /** Open this dock panel. */
  panel?: string;
  /** Hash route inside the panel, e.g. "#/clusters". */
  at?: string;
}

// FollowupGroup offers the next step after a turn. `when` is matched against
// what the last turn did (its tool calls and reply); no match shows nothing.
export interface FollowupGroup {
  when: string[];
  chips: StarterCard[];
}

export interface Starters {
  heading?: string;
  /** Send a prompt card on click instead of filling the composer. */
  send?: boolean;
  cards: StarterCard[];
  followups?: FollowupGroup[];
}

// PanelInfo is an external web app registered with -web-panel. path is the
// same-origin prefix the iframe loads (/panels/<name>/); the backend URL is
// never exposed to the page.
export interface PanelInfo {
  name: string;
  path: string;
}

export interface ModelInfo {
  id: string;
  provider: string;
  aliases?: string[];
  context_window: number;
  configured: boolean;
  /**
   * The model a read-only subagent of this one runs, from ~/.pi-go/providers.json.
   * Worth showing because it is the one catalogue entry that changes what happens
   * without anyone naming it at a prompt.
   */
  subagent_model?: string;
  /**
   * Which environment variable to set, sent only when `configured` is false. The
   * terminal prints its own hint; a browser cannot, so the server names it.
   */
  key_env?: string;
}

export const emptyLive = (): Live => ({
  active: false,
  pending_tools: [],
  pending_gates: [],
});

// --- Workspace file panel (web-ui-design.md §16) ---------------------------

export interface FileEntry {
  name: string;
  dir: boolean;
  size: number;
  mtime_ms: number;
}

export interface FilesResponse {
  path: string;
  entries: FileEntry[];
  truncated: boolean;
}

// FileContent has two shapes: binary files report {binary, mime, size} only;
// text files carry {text, truncated, ...}. The presence of `binary` is the
// discriminant.
export interface FileContent {
  path: string;
  binary?: boolean;
  mime?: string;
  text?: string;
  truncated?: boolean;
  truncated_by?: "lines" | "bytes" | "";
  size: number;
  mtime_ms?: number;
}

// --- Version control state (web-ui-design.md §18.6) ------------------------

// GitStatus mirrors git.Status. Counts, not paths: the panel below already lists
// files, and a list here would grow without bound with the repository.
export interface GitStatus {
  repo: boolean;
  root?: string;
  branch?: string;
  detached?: boolean;
  unborn?: boolean;
  head?: string;
  subject?: string;
  upstream?: string;
  ahead: number;
  behind: number;
  staged: number;
  unstaged: number;
  untracked: number;
  conflicted: number;
  // Why there is no answer, when the reason is neither "no repository" nor a
  // real state — no git binary, a timeout, a broken repository.
  unavailable?: string;
}

// --- Workspace-level changes (journal-backed, web-ui-design.md §16 M4) -----

export interface WorkspaceChange {
  path: string;
  status: "added" | "modified" | "deleted";
  added: number;
  removed: number;
  first_ms: number;
  last_ms: number;
  sid: string;
  base_available: boolean;
}

export interface WorkspaceDiff {
  path: string;
  status: string;
  added: number;
  removed: number;
  base_available: boolean;
  /** too_big means the patch exists on disk but was withheld from the UI. */
  too_big?: boolean;
  patch?: string;
}

export interface FileIndexResponse {
  paths: string[];
  capped: boolean;
}
