import { kit, parseJsonText, utf8Length } from "../core/validate";
import type { Intent } from "./client";

export const PENDING_ACTIONS_KEY = "tbound.live.pending";
export const MAX_PENDING_ACTIONS = 16;
export const MAX_PENDING_BYTES = 1024 * 1024;

export interface StoredIntent {
  readonly sessionId: string | null;
  readonly intent: Intent;
}

const { asRecord, closedKeys, stringValue, countValue, boolValue, fail } = kit;

function parseStoredIntent(value: unknown): StoredIntent {
  const entry = asRecord("pending", value);
  closedKeys("pending", entry, ["sessionId", "intent"]);
  const sessionId = entry.sessionId === null ? null : stringValue("sessionId", entry.sessionId);
  if (sessionId !== null && !/^psess-[A-Za-z0-9_-]{1,128}$/.test(sessionId)) fail("sessionId", "invalid session");
  const raw = asRecord("intent", entry.intent);
  closedKeys("intent", raw, ["kind", "key", "path", "body"]);
  const key = stringValue("key", raw.key);
  if (!/^ck-[0-9a-f]{32}$/.test(key)) fail("key", "invalid request key");
  const path = stringValue("path", raw.path);
  const body = asRecord("body", raw.body);
  if (body.request_key !== key || utf8Length(JSON.stringify(body)) > 64 * 1024) fail("body", "invalid request body");
  const kind = raw.kind;
  if (kind === "create") {
    if (sessionId !== null || path !== "/api/v1/sessions") fail("path", "invalid create route");
    closedKeys("body", body, ["project_id", "provider_profile", "publication_mode", "budget", "read_only", "request_key"]);
    for (const field of ["project_id", "provider_profile", "publication_mode"]) stringValue(field, body[field]);
    const budget = asRecord("budget", body.budget);
    closedKeys("budget", budget, ["host_calls", "tokens", "duration_seconds"]);
    for (const field of ["host_calls", "tokens", "duration_seconds"]) countValue(field, budget[field]);
    if (body.read_only !== undefined) boolValue("read_only", body.read_only);
  } else {
    if (sessionId === null) return fail("sessionId", "required for this operation");
    countValue("expected_revision", body.expected_revision);
    const base = `/api/v1/sessions/${encodeURIComponent(sessionId)}/turns`;
    if (kind === "submit") {
      closedKeys("body", body, ["expected_revision", "request_key", "prompt"]);
      stringValue("prompt", body.prompt);
      if (path !== base) fail("path", "invalid submit route");
    } else if (kind === "cancel" || kind === "resolve") {
      const suffix = path.slice(base.length);
      if (!path.startsWith(base) || !new RegExp(`^/turn-[1-9][0-9]*/${kind}$`).test(suffix)) fail("path", "invalid turn route");
      closedKeys("body", body, kind === "cancel"
        ? ["expected_revision", "request_key"]
        : ["expected_revision", "request_key", "candidate_digest", "decision"]);
      if (kind === "resolve") {
        kit.referenceValue("candidate_digest", body.candidate_digest, false);
        if (body.decision !== "publish" && body.decision !== "discard") fail("decision", "invalid decision");
      }
    } else {
      return fail("kind", "invalid operation");
    }
  }
  return { sessionId, intent: { kind, key, path, body } };
}

export function readStoredIntents(text: string | null): readonly StoredIntent[] {
  if (text === null || text.length > MAX_PENDING_BYTES || utf8Length(text) > MAX_PENDING_BYTES) return [];
  try {
    const entries = kit.arrayValue("pending", parseJsonText(text), MAX_PENDING_ACTIONS).map(parseStoredIntent);
    if (new Set(entries.map((entry) => entry.sessionId)).size !== entries.length) return [];
    return entries;
  } catch {
    return [];
  }
}
