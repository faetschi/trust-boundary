import { describe, expect, it } from "vitest";
import type { LiveSessionView, LiveTurn } from "../core/contract";
import { LiveError } from "./client";
import { DEFAULT_FIELDS, createParams } from "./LiveSessions";
import { actionText, budgetExhausted, cancelGate, failureText, openTurnOf, phaseOf, remainingBudget, retryable, reviewableTurn, sendGate } from "./readings";

const turn = (patch: Partial<LiveTurn>): LiveTurn => ({ id: "psess-1/turn-1", ordinal: 1, state: "admitted", prompt: "p", ...patch });

function view(turns: LiveTurn[], patch: Partial<LiveSessionView> = {}): LiveSessionView {
  const zero = { host_calls: 0, tokens: 0, duration_seconds: 0 };
  return {
    id: "psess-1",
    project_id: "demo",
    provider_profile: "prof",
    publication_mode: "review",
    revision: 3,
    budget: { host_calls: 10, tokens: 1000, duration_seconds: 60 },
    consumed: zero,
    reserved: zero,
    total_turns: turns.length,
    turns,
    ...patch,
  };
}

const IDLE = { status: "idle" } as const;

describe("live readings: the phase of a turn", () => {
  it.each([
    [{ state: "admitted" }, "running"],
    [{ state: "linked" }, "running"],
    [{ state: "linked", cancel_requested: true }, "cancelling"],
    [{ state: "awaiting_review", candidate_digest: "sha256:c" }, "review"],
    [{ state: "awaiting_review", resolution: "publish" }, "resolving"],
    [{ state: "awaiting_review", cancel_requested: true }, "cancelling"],
    [{ state: "terminal", outcome: "published" }, "done"],
    [{ state: "abandoned" }, "abandoned"],
    [{ state: "unknown" }, "unknown"],
    [{ state: "a state this page has never heard of" }, "unknown"],
  ] as const)("%j reads as %s", (patch, phase) => {
    expect(phaseOf(turn(patch))).toBe(phase);
  });

  it("finds the latest open turn, and the one that can be reviewed", () => {
    const settled = view([turn({ state: "terminal" }), turn({ id: "psess-1/turn-2", ordinal: 2, state: "abandoned" })]);
    expect(openTurnOf(settled)).toBeNull();
    const review = view([turn({ state: "terminal" }), turn({ id: "psess-1/turn-2", ordinal: 2, state: "awaiting_review", candidate_digest: "sha256:c" })]);
    expect(openTurnOf(review)?.phase).toBe("review");
    expect(reviewableTurn(review)?.id).toBe("psess-1/turn-2");
    expect(reviewableTurn(view([turn({ state: "awaiting_review" })]))).toBeNull();
    expect(reviewableTurn(view([turn({ state: "awaiting_review", candidate_digest: "sha256:c", resolution: "discard" })]))).toBeNull();
    expect(reviewableTurn(null)).toBeNull();
  });
});

describe("live readings: remaining budget", () => {
  it("is the budget less what is consumed and reserved", () => {
    const left = remainingBudget(
      view([], {
        consumed: { host_calls: 3, tokens: 100, duration_seconds: 10 },
        reserved: { host_calls: 2, tokens: 50, duration_seconds: 5 },
      }),
    );
    expect(left).toEqual({ host_calls: 5, tokens: 850, duration_seconds: 45 });
    expect(budgetExhausted(left)).toBe(false);
  });

  it("never goes below zero, and says when one kind is used up", () => {
    const left = remainingBudget(
      view([], { consumed: { host_calls: 9, tokens: 0, duration_seconds: 0 }, reserved: { host_calls: 5, tokens: 0, duration_seconds: 0 } }),
    );
    expect(left.host_calls).toBe(0);
    expect(budgetExhausted(left)).toBe(true);
  });
});

describe("live readings: what may be sent", () => {
  it("allows a prompt in an idle session and gives a reason in every other case", () => {
    expect(sendGate(view([]), IDLE)).toEqual({ allowed: true, reason: null });
    expect(sendGate(view([turn({ state: "terminal" })]), IDLE).allowed).toBe(true);
    expect(sendGate(null, IDLE).reason).toContain("loading");
    expect(sendGate(view([turn({ state: "linked" })]), IDLE).reason).toBe("A turn is in progress.");
    expect(sendGate(view([turn({ state: "awaiting_review", candidate_digest: "sha256:c" })]), IDLE).reason).toContain("review");
    expect(sendGate(view([turn({ state: "unknown" })]), IDLE).reason).toContain("unknown outcome");
    expect(sendGate(view([], { blocked: true }), IDLE).reason).toContain("blocked");
    expect(sendGate(view([]), { status: "sending" }).allowed).toBe(false);
    expect(sendGate(view([]), { status: "unknown" }).reason).toContain("Retry it or dismiss it");
    expect(sendGate(view([]), { status: "failed" }).allowed).toBe(true);
  });

  it("allows cancel for a running turn or one in review, once", () => {
    expect(cancelGate(view([turn({ state: "linked" })]), IDLE).allowed).toBe(true);
    expect(cancelGate(view([turn({ state: "awaiting_review", candidate_digest: "sha256:c" })]), IDLE).allowed).toBe(true);
    expect(cancelGate(view([turn({ state: "linked", cancel_requested: true })]), IDLE).reason).toContain("already");
    expect(cancelGate(view([turn({ state: "terminal" })]), IDLE).reason).toBe("No turn is running.");
    expect(cancelGate(view([turn({ state: "unknown" })]), IDLE).allowed).toBe(false);
    expect(cancelGate(view([turn({ state: "linked" })]), { status: "sending" }).allowed).toBe(false);
  });
});

describe("live readings: failures in words", () => {
  const error = (patch: Partial<ConstructorParameters<typeof LiveError>[0]>) =>
    new LiveError({ kind: "service", status: 409, code: "STALE_REVISION", detail: "", outcomeUnknown: false, ...patch });

  it("keeps the stable code beside the words, and the service's detail after them", () => {
    expect(failureText(error({}))).toContain("(STALE_REVISION)");
    expect(failureText(error({ code: "TURN_ACTIVE", detail: "psess-1/turn-1 is running" }))).toBe(
      "A turn is already running in this session. (TURN_ACTIVE). psess-1/turn-1 is running",
    );
    expect(failureText(error({ code: "SOMETHING_NEW", status: 502, detail: "" }))).toBe("SOMETHING_NEW. HTTP 502.");
  });

  it("offers a repeat only where nothing ran or it is not known", () => {
    expect(retryable(error({}))).toBe(false);
    expect(retryable(error({ outcomeUnknown: true, status: 503 }))).toBe(true);
    expect(retryable(error({ kind: "transport", status: 0, code: "TRANSPORT" }))).toBe(true);
    expect(retryable(error({ status: 503, code: "SERVICE_UNAVAILABLE" }))).toBe(true);
  });

  it("words the notice for each state of the request", () => {
    const base = { intent: null, attempt: 1, done: null } as const;
    expect(actionText({ ...base, status: "idle", error: null })).toBeNull();
    expect(actionText({ ...base, status: "sending", error: null })).toBeNull();
    expect(actionText({ ...base, status: "sending", attempt: 2, error: error({}) })).toContain("same request key (attempt 2)");
    expect(actionText({ ...base, status: "failed", error: error({}) })).toContain("STALE_REVISION");
    expect(actionText({ ...base, status: "unknown", error: error({ kind: "transport", status: 0, code: "TRANSPORT" }) })).toContain("Outcome unknown");
    expect(actionText({ ...base, status: "idle", error: null, done: { kind: "submit", replayed: true } })).toContain("already recorded");
  });
});

describe("live readings: the new session form", () => {
  const fields = { ...DEFAULT_FIELDS, project: "demo", provider: "prof" };

  it("builds the create request from valid fields", () => {
    expect(createParams(fields)).toEqual({
      projectId: "demo",
      providerProfile: "prof",
      publicationMode: "review",
      hostCalls: 50,
      tokens: 100000,
      durationSeconds: 600,
      readOnly: false,
    });
  });

  it("names the first problem", () => {
    expect(createParams({ ...fields, project: "" })).toContain("Project is required");
    expect(createParams({ ...fields, project: "two words" })).toContain("Project is required");
    expect(createParams({ ...fields, provider: "" })).toContain("Provider profile is required");
    expect(createParams({ ...fields, hostCalls: "0" })).toContain("Host calls must be a positive whole number");
    expect(createParams({ ...fields, tokens: "1.5" })).toContain("Tokens must be a positive whole number");
    expect(createParams({ ...fields, seconds: "-3" })).toContain("Seconds must be a positive whole number");
  });
});
