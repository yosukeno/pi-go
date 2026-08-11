import type { AgentEvent } from "@/api/types";

// Frame parsing lives on its own, with no imports beyond the types, so it can be
// tested without a DOM, a token, or Vue.

/** parseFrame turns one SSE frame into an event, or null for a keepalive. */
export function parseFrame(frame: string): AgentEvent | null {
  let data = "";
  for (const raw of frame.split("\n")) {
    const line = raw.replace(/\r$/, "");
    if (line.startsWith(":")) continue; // keepalive comment
    if (line.startsWith("data:")) data += line.slice(5).trimStart();
  }
  if (!data) return null;
  try {
    return JSON.parse(data) as AgentEvent;
  } catch {
    // One malformed frame must not kill the stream.
    return null;
  }
}
