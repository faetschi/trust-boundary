// readings.ts — what the live session view means, as pure functions. No DOM, no React, no timer.
//
// The view's turn states are the service's own words. A state this page does not know reads as "unknown", which blocks
// sending, so an unfamiliar state can only make the page more careful. Nothing here decides anything the service
// decides: the gates only keep the page from asking for what the service would refuse, and the service still refuses.

import type { LiveBudget, LiveSessionView, LiveTurn } from "../core/contract";
import type { LiveError } from "./client";
import type { ActionState } from "./controller";

export type TurnPhase = "running" | "cancelling" | "review" | "resolving" | "unknown" | "abandoned" | "done";

export function phaseOf(turn: LiveTurn): TurnPhase {
  switch (turn.state) {
    case "admitted":
    case "linked":
      return turn.cancel_requested === true ? "cancelling" : "running";
    case "awaiting_review":
      if (turn.cancel_requested === true) return "cancelling";
      return turn.resolution ? "resolving" : "review";
    case "terminal":
      return "done";
    case "abandoned":
      return "abandoned";
    default:
      return "unknown";
  }
}

export interface OpenTurn {
  readonly turn: LiveTurn;
  readonly phase: TurnPhase;
}

// The latest turn that is not settled. A settled turn is terminal or abandoned.
export function openTurnOf(view: LiveSessionView | null): OpenTurn | null {
  const turns = view?.turns ?? [];
  for (let index = turns.length - 1; index >= 0; index--) {
    const turn = turns[index];
    if (turn === undefined) continue;
    const phase = phaseOf(turn);
    if (phase !== "done" && phase !== "abandoned") return { turn, phase };
  }
  return null;
}

// The turn whose candidate the user may publish or discard: awaiting review, with no decision or cancel requested.
export function reviewableTurn(view: LiveSessionView | null): LiveTurn | null {
  const open = openTurnOf(view);
  return open !== null && open.phase === "review" && open.turn.candidate_digest ? open.turn : null;
}

// What is left to spend: the budget less what is consumed and what a running turn has reserved, never below zero.
export function remainingBudget(view: LiveSessionView): LiveBudget {
  const left = (budget: number, consumed: number, reserved: number) => Math.max(0, budget - consumed - reserved);
  return {
    host_calls: left(view.budget.host_calls, view.consumed.host_calls, view.reserved.host_calls),
    tokens: left(view.budget.tokens, view.consumed.tokens, view.reserved.tokens),
    duration_seconds: left(view.budget.duration_seconds, view.consumed.duration_seconds, view.reserved.duration_seconds),
  };
}

export function budgetExhausted(left: LiveBudget): boolean {
  return left.host_calls === 0 || left.tokens === 0 || left.duration_seconds === 0;
}

export interface Gate {
  readonly allowed: boolean;
  // Why not, in words. Null when allowed.
  readonly reason: string | null;
}

const OPEN: Gate = { allowed: true, reason: null };
const closed = (reason: string): Gate => ({ allowed: false, reason });

export interface ActionBasis {
  readonly status: "idle" | "sending" | "failed" | "unknown";
}

// Whether a prompt may be sent now. One mutation is in flight at a time, an unknown outcome must be settled first (retried
// under its own key), and the service allows one open turn per session.
export function sendGate(view: LiveSessionView | null, action: ActionBasis): Gate {
  if (view === null) return closed("The session is loading.");
  if (action.status === "sending") return closed("A request is being sent.");
  if (action.status === "unknown") return closed("A request has an unknown outcome. Retry it with the same request key before sending another prompt.");
  if (view.blocked === true) return closed("This session is blocked. An earlier turn has an unknown outcome, and an operator must reconcile it.");
  const open = openTurnOf(view);
  if (open === null) return OPEN;
  switch (open.phase) {
    case "review":
      return closed("A turn is waiting for your review. Publish or discard its changes first.");
    case "unknown":
      return closed("A turn has an unknown outcome. An operator must reconcile it.");
    default:
      return closed("A turn is in progress.");
  }
}

// Whether the open turn can be cancelled: running, or waiting for review.
export function cancelGate(view: LiveSessionView | null, action: ActionBasis): Gate {
  if (view === null) return closed("The session is loading.");
  if (action.status === "sending" || action.status === "unknown") return closed("A request is pending.");
  const open = openTurnOf(view);
  if (open === null) return closed("No turn is running.");
  if (open.phase === "cancelling") return closed("Cancel was already requested.");
  return open.phase === "running" || open.phase === "review" ? OPEN : closed("This turn cannot be cancelled now.");
}

// The service's refusal codes, in words. The code stays visible beside them: the stable code is the contract.
const FRIENDLY: Readonly<Record<string, string>> = {
  STALE_REVISION: "The session changed since this page last read it. It was read again; send again if you still want to.",
  STALE_BASELINE: "The project changed since the session read it.",
  TURN_ACTIVE: "A turn is already running in this session.",
  PENDING_REVIEW: "A turn in this session is waiting for review.",
  PROJECT_BUSY: "Another session is using this project.",
  PROJECT_BLOCKED: "An earlier turn left this project with an unknown outcome. An operator must reconcile it.",
  PREDECESSOR_UNRESOLVED: "An earlier turn is not resolved.",
  BUDGET_EXHAUSTED: "The session budget is used up.",
  BUDGET_EXCEEDS_LIMIT: "The budget is above what this project allows.",
  CANDIDATE_MISMATCH: "The candidate changed after it was shown. Review the new changes.",
  CANDIDATE_TOO_LARGE: "The complete candidate preview exceeds the service's 1 MiB response limit. No paths were omitted. Choose Cancel turn to discard this awaiting-review turn, or ask an operator for help.",
  REVIEW_NOT_PENDING: "There is no review to decide.",
  CANCEL_NOT_ALLOWED: "This turn cannot be cancelled now.",
  CANCELLED: "The turn was cancelled.",
  REQUEST_CONFLICT: "This request key was already used for a different request.",
  UNKNOWN_PROJECT: "The service does not know this project.",
  UNKNOWN_PROVIDER_PROFILE: "The service does not know this provider profile.",
  PUBLICATION_MODE_NOT_PERMITTED: "This project does not allow that publication mode.",
  UNAUTHENTICATED: "This browser is not paired with the gateway, or its pairing ended.",
  FORBIDDEN: "The gateway refused the request. Reload the page.",
  SERVICE_UNAVAILABLE: "The product service could not be reached.",
  TRANSPORT: "The gateway could not be reached.",
};

export function failureText(error: LiveError): string {
  const words = FRIENDLY[error.code];
  const tail = error.detail && error.detail !== `HTTP ${error.status}` ? ` ${error.detail}` : "";
  return words ? `${words} (${error.code}).${tail}` : `${error.code}.${tail || ` HTTP ${error.status}.`}`;
}

// Whether sending the same intent again is safe and useful: nothing ran, or it is not known whether anything did, and the
// request key makes the repeat idempotent. A definite refusal (a 4xx) would only be refused again.
export function retryable(error: LiveError): boolean {
  return error.outcomeUnknown || error.kind === "transport" || error.status === 503;
}

// The one-line state of a turn for a list or a notice.
export function phaseWords(phase: TurnPhase): string {
  switch (phase) {
    case "running":
      return "running";
    case "cancelling":
      return "cancel requested";
    case "review":
      return "awaiting your review";
    case "resolving":
      return "decision recorded";
    case "unknown":
      return "outcome unknown";
    case "abandoned":
      return "abandoned";
    case "done":
      return "finished";
  }
}

// The notice for the mutation that is in flight, failed or unsettled, or null when there is nothing to say.
export function actionText(action: ActionState): string | null {
  switch (action.status) {
    case "sending":
      return action.attempt > 1 ? `Sending again with the same request key (attempt ${action.attempt}).` : null;
    case "failed":
      return action.error === null ? null : failureText(action.error);
    case "unknown":
      return action.error === null
        ? null
        : `Outcome unknown. This page cannot tell whether the request ran. ${action.intent?.kind === "resolve" ? "The review decision request still needs confirmation, even if the turn has finished. Retry to confirm its result before starting new work. " : ""}Retrying sends the same request key, so it cannot run twice. ${failureText(action.error)}`;
    case "idle":
      return action.done?.replayed === true ? "The service had already recorded this request." : null;
  }
}
