import type {
  FileContent,
  FileIndexResponse,
  FilesResponse,
  GitStatus,
  ModelInfo,
  PanelInfo,
  Starters,
  PolicyMode,
  PolicyState,
  SessionInfo,
  SkillInfo,
  Snapshot,
  WorkspaceChange,
  WorkspaceDiff,
} from "./types";

// The token arrives as a query parameter on the URL the server prints at
// startup. It is kept in sessionStorage so a reload without the query string
// still works, and mirrored back into the URL so the tab can be duplicated.
// When it is absent or rejected, boot stops at the token gate (TokenGate.vue),
// whose submit is the only other writer — via setToken below.
const TOKEN_KEY = "pi-go-token";

function readToken(): string {
  // Guarded so importing this module outside a browser (a unit test) is harmless.
  if (typeof location === "undefined") return "";
  const fromUrl = new URLSearchParams(location.search).get("token");
  if (fromUrl) {
    sessionStorage.setItem(TOKEN_KEY, fromUrl);
    return fromUrl;
  }
  return sessionStorage.getItem(TOKEN_KEY) ?? "";
}

export const token = readToken();

export function authHeaders(): HeadersInit {
  return token ? { Authorization: `Bearer ${token}` } : {};
}

// The token gate's way in: write the token everywhere readToken looks, then
// the caller reloads the page — `token` above is fixed at import time.
export function setToken(value: string) {
  // Guarded like readToken so unit tests can import this module.
  if (typeof location === "undefined") return;
  sessionStorage.setItem(TOKEN_KEY, value);
  const url = new URL(location.href);
  url.searchParams.set("token", value);
  history.replaceState(null, "", url);
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: {
      ...authHeaders(),
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  // Error bodies are not promised to be JSON — the auth middleware's 401 is
  // plain text — so a parse failure must not become the message the user sees.
  let data: { error?: string } | undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }
  if (!res.ok) {
    const message = data?.error ?? (text || `HTTP ${res.status}`);
    throw Object.assign(new Error(message), { status: res.status });
  }
  return data as T;
}

export const api = {
  models: () => request<{ models: ModelInfo[]; default: string }>("GET", "/api/models"),

  // Server-wide and fixed at startup, so this is fetched once on mount and never
  // refreshed.
  skills: () => request<{ skills: SkillInfo[] }>("GET", "/api/skills"),

  // External panels registered with -web-panel; same once-at-boot fetch rule
  // as skills.
  panels: () => request<{ panels: PanelInfo[] }>("GET", "/api/panels"),

  // Empty-state cards contributed by the loaded skills. Fetched at boot like
  // the two above; the server re-reads the files each time, so a reload is
  // enough to pick up an edited starters.json.
  starters: () => request<{ starters: Starters }>("GET", "/api/starters"),

  sessions: () => request<{ sessions: SessionInfo[]; cwd: string }>("GET", "/api/sessions"),

  // workspace is a directory relative to the server root; "" or omitted picks
  // the root itself.
  createSession: (model?: string, workspace?: string) =>
    request<{ session_id: string; path: string; model: string; provider: string; workspace: string }>(
      "POST",
      "/api/sessions",
      { model, workspace },
    ),

  session: (sid: string) => request<{ session_id: string; snapshot: Snapshot }>("GET", `/api/sessions/${sid}`),

  deleteSession: (sid: string) => request<void>("DELETE", `/api/sessions/${sid}`),

  // Pin and rename are sidebar edits; either field may be sent on its own.
  updateSession: (sid: string, patch: { title?: string; pinned?: boolean }) =>
    request<void>("PATCH", `/api/sessions/${sid}`, patch),

  // Returns 409 when a run is already in progress; the caller surfaces that as
  // "the agent is busy" rather than retrying.
  send: (sid: string, prompt: string) =>
    request<{ run_id: string }>("POST", `/api/sessions/${sid}/messages`, { prompt }),

  // Cancelling goes through this endpoint rather than aborting the SSE
  // connection: dropping a socket would leave the bash child process running.
  cancel: (sid: string) => control<{ cancelled: boolean }>(sid, { action: "cancel" }),

  // Rewind forks the transcript away from a user message: the message and
  // everything after it leave the branch, and the caller then refills the
  // composer with the withdrawn text.
  //
  // mode says what to act on: "chat" forks only, "files" restores the workspace
  // to the checkpoint taken when the message was sent and leaves the conversation
  // alone, "both" does both. paths narrows a file restore to a subset of what the
  // preview listed; omit it for all of them.
  //
  // 409 while a run is in flight, 404 when the timeline id is unknown, 422 when
  // files were asked for but no checkpoint exists for that point.
  rewind: (sid: string, messageId: string, mode: "chat" | "files" | "both", paths?: string[]) =>
    control<{ rewound: boolean }>(sid, {
      action: "rewind",
      message_id: messageId,
      mode,
      ...(paths && paths.length ? { paths } : {}),
    }),

  // Replace the conversation with a summary of it. Costs one model call, so this
  // can take as long as a turn does; 409 means a run is in flight and 422 means
  // compaction would not make the prompt smaller.
  compact: (sid: string) =>
    control<{
      compacted: boolean;
      before: number;
      before_tokens: number;
      after_tokens: number;
      freed_tokens: number;
      summary: string;
    }>(sid, { action: "compact" }),

  // What a rewind-with-files would restore: the modified/deleted/created
  // paths between the checkpoint and now, each with the line counts the
  // restore itself would add and remove (-1/-1 for binary). available is
  // false when the point predates checkpointing, and the dialog then offers
  // conversation-only.
  rewindPreview: (sid: string, messageId: string) =>
    control<{ available: boolean; changes: { path: string; status: string; added: number; removed: number }[] }>(sid, {
      action: "rewind_preview",
      message_id: messageId,
    }),

  // Steering joins the run in flight. It goes through /control rather than
  // /messages because it must never start one: `steered: false` means there was
  // nothing to join and the caller should send an ordinary message instead.
  steer: (sid: string, prompt: string) => control<{ steered: boolean }>(sid, { action: "steer", prompt }),

  setModel: (sid: string, model: string) =>
    control<{ model: string; provider: string }>(sid, { action: "set_model", model }),

  setPolicy: (sid: string, mode: PolicyMode, turns?: number) =>
    control<PolicyState>(sid, { action: "set_policy", mode, turns }),

  allowTool: (sid: string, tool: string) =>
    control<PolicyState>(sid, { action: "set_policy", allow_tool: tool }),

  allowCommand: (sid: string, command: string) =>
    control<PolicyState>(sid, { action: "set_policy", allow_command: command }),

  decideGate: (
    sid: string,
    gateId: string,
    allow: boolean,
    opts: { args?: unknown; reason?: string; remember?: "tool" | "command" } = {},
  ) => control<{ ok: true }>(sid, { action: "gate_decide", gate_id: gateId, allow, ...opts }),

  // Freeze stops the server-side countdown while the user edits the arguments;
  // thaw resumes it from where it stopped and publishes the new deadline.
  freezeGate: (sid: string, gateId: string) => control<{ ok: true }>(sid, { action: "gate_freeze", gate_id: gateId }),

  thawGate: (sid: string, gateId: string) => control<{ ok: true }>(sid, { action: "gate_thaw", gate_id: gateId }),

  // The file panel's read-only workspace API (web-ui-design §16). Paths are
  // workspace-relative; the server refuses escapes with the same check the
  // agent's own tools get.
  files: (path: string) => request<FilesResponse>("GET", `/api/files?path=${encodeURIComponent(path)}`),

  // The quick-open path index: whole workspace in one shot, fuzzy filtering
  // happens client-side.
  fileIndex: () => request<FileIndexResponse>("GET", "/api/files/index"),

  fileContent: (path: string) =>
    request<FileContent>("GET", `/api/files/content?path=${encodeURIComponent(path)}`),

  // The user's own edit from the panel, not the agent's: optimistic mtime
  // concurrency, 409 when the file moved on disk since it was read.
  saveFile: (path: string, text: string, baseMtimeMs: number, force = false) =>
    request<{ path: string; size: number; mtime_ms: number }>("PUT", "/api/files/content", {
      path,
      text,
      base_mtime_ms: baseMtimeMs,
      force,
    }),

  // One level at a time; 409 when it exists, 404 when the parent does not.
  mkdir: (path: string) => request<{ path: string }>("POST", "/api/files/mkdir", { path }),

  // Version control state. Read-only and always 200: "not a repository" is a
  // state to render, not an error (§18.6).
  workspaceGit: () => request<GitStatus>("GET", "/api/workspace/git"),

  // Workspace-level changes, journaled against first-touch pre-images (§16 M4).
  workspaceChanges: () => request<{ changes: WorkspaceChange[] }>("GET", "/api/workspace/changes"),

  workspaceDiff: (path: string) =>
    request<WorkspaceDiff>("GET", `/api/workspace/diff?path=${encodeURIComponent(path)}`),

  // Reset the baseline: diffs accumulate from this moment on.
  clearWorkspaceJournal: () => request<{ ok: true }>("POST", "/api/workspace/journal/clear"),
};

// fileRawURL builds the <img> src for a workspace image. The token rides in
// the query because an img tag cannot set headers — the server accepts it
// there for exactly this kind of client, and the raw endpoint only serves
// sniffed image/* for the same origin-safety reason.
export function fileRawURL(path: string): string {
  return `/api/files/content?path=${encodeURIComponent(path)}&raw=1&token=${encodeURIComponent(token)}`;
}

function control<T>(sid: string, body: Record<string, unknown>): Promise<T> {
  return request<T>("POST", `/api/sessions/${sid}/control`, body);
}

export function streamURL(sid: string, from?: number): string {
  const params = new URLSearchParams();
  if (from !== undefined) params.set("from", String(from));
  const q = params.toString();
  return `/api/sessions/${sid}/stream${q ? `?${q}` : ""}`;
}
