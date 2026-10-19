// builders.ts — test-only constructors for contract-shaped values. A builder returns a complete
// object of its type with contract-plausible defaults, so a test can name only the fields it is
// about. Defaults are the unverified case (diagnostic, unverified-input); a test that needs a
// verified value sets authority and verification_scope itself.
//
// These builders do NOT validate. A test that needs an invalid value passes it through the
// parser (validate.ts) or uses an explicit @ts-expect-error with a reason.

import {
  CONTRACT_VERSION,
  type ActivityItem,
  type Authority,
  type ItemBase,
  type DecisionItem,
  type MessageItem,
  type Panels,
  type ResultItem,
  type ResultState,
  type Role,
  type SessionHeader,
  type Snapshot,
  type SourceClass,
  type StreamStatus,
  type Tool,
  type Verdict,
  type VerificationScope,
} from "../contract";

export const TIMESTAMP = "2026-10-13T09:12:04Z";

interface Common {
  readonly id: string;
  readonly sequence: number;
  readonly turn_id?: string;
  readonly authority?: Authority;
  readonly verification_scope?: VerificationScope;
  readonly occurred_at?: string;
}

function common(input: Common): ItemBase {
  return {
    schema_version: CONTRACT_VERSION,
    id: input.id,
    sequence: input.sequence,
    turn_id: input.turn_id ?? "t1",
    occurred_at: input.occurred_at ?? TIMESTAMP,
    authority: input.authority ?? ("diagnostic" as const),
    verification_scope: input.verification_scope ?? ("unverified-input" as const),
  };
}

export function messageItem(
  input: Common & { readonly role?: Role; readonly text?: string; readonly text_truncated?: boolean },
): MessageItem {
  const text = input.text ?? `m${input.sequence}`;
  const base = { ...common(input), type: "message" as const, role: input.role ?? "assistant" };
  if (input.text_truncated === true && text === "") return { ...base, text_truncated: true };
  return { ...base, text, ...(input.text_truncated !== undefined ? { text_truncated: input.text_truncated } : {}) };
}

export function activityItem(
  input: Common & {
    readonly tool: Tool;
    readonly tool_call_id: string;
    readonly state: ResultState;
    readonly kind?: string;
    readonly summary?: string;
    readonly detail?: string;
  },
): ActivityItem {
  return {
    ...common(input),
    type: "activity",
    kind: input.kind ?? "proposal",
    tool: input.tool,
    tool_call_id: input.tool_call_id,
    summary: input.summary ?? `${input.tool} call`,
    state: input.state,
    ...(input.detail !== undefined ? { detail: input.detail } : {}),
  };
}

export function decisionItem(
  input: Common & {
    readonly tool: Tool;
    readonly tool_call_id: string;
    readonly verdict: Verdict;
    readonly reason_code?: string;
    readonly policy_digest?: string;
    readonly summary?: string;
  },
): DecisionItem {
  return {
    ...common(input),
    type: "decision",
    tool: input.tool,
    tool_call_id: input.tool_call_id,
    verdict: input.verdict,
    reason_code: input.reason_code ?? "policy_rule_allow",
    ...(input.policy_digest !== undefined ? { policy_digest: input.policy_digest } : {}),
    ...(input.summary !== undefined ? { summary: input.summary } : {}),
  };
}

export function resultItem(
  input: Common & {
    readonly tool: Tool;
    readonly tool_call_id: string;
    readonly state: ResultState;
    readonly summary?: string;
    readonly detail?: string;
    readonly digest?: string;
    readonly lossy?: boolean;
  },
): ResultItem {
  return {
    ...common(input),
    type: "result",
    tool: input.tool,
    tool_call_id: input.tool_call_id,
    state: input.state,
    ...(input.summary !== undefined ? { summary: input.summary } : {}),
    ...(input.detail !== undefined ? { detail: input.detail } : {}),
    ...(input.digest !== undefined ? { digest: input.digest } : {}),
    ...(input.lossy !== undefined ? { lossy: input.lossy } : {}),
  };
}

export function panelsDefault(): Panels {
  return {
    governance: { allow_count: 0, deny_count: 0, unknown_count: 0 },
    provenance: { available: false },
    publication: { state: "not-attempted" },
    recovery: { state: "not-required" },
    qualification: { g1: false, h1: "unexecuted", containment: "not-established", claim_bearing: false },
  };
}

export function sessionDefault(): SessionHeader {
  return { session_id: "s1", status: "pending", turn_count: 1 };
}

export function streamDefault(overrides: Partial<StreamStatus> & Pick<StreamStatus, "id">): StreamStatus {
  return { label: overrides.id, health: "observing", available: true, ...overrides };
}

export interface SnapshotInput {
  readonly epoch?: string;
  readonly cursor?: string;
  readonly source?: SourceClass;
  readonly verification_scope?: VerificationScope;
  readonly items?: Snapshot["items"];
  readonly streams?: readonly StreamStatus[];
  readonly gaps?: Snapshot["gaps"];
  readonly warnings?: readonly string[];
  readonly session?: SessionHeader;
  readonly panels?: Panels;
}

// A snapshot whose cursor ends at its last item's sequence, as the gateway serves it.
export function snapshotOf(input: SnapshotInput = {}): Snapshot {
  const epoch = input.epoch ?? "e1";
  const items = input.items ?? [];
  const last = items[items.length - 1];
  return {
    schema_version: CONTRACT_VERSION,
    epoch,
    cursor: input.cursor ?? `${epoch}:${last ? last.sequence : 0}`,
    source: input.source ?? "fixture",
    verification_scope: input.verification_scope ?? "unverified-input",
    generated_at: TIMESTAMP,
    session: input.session ?? sessionDefault(),
    panels: input.panels ?? panelsDefault(),
    streams: input.streams ?? [],
    items,
    gaps: input.gaps ?? [],
    warnings: input.warnings ?? [],
  };
}
