// The live panel above the transcript: the remaining budget, the state of the open turn, and a blocked session. LiveReview,
// at the foot of the transcript, is the review of a candidate. Both are bordered regions of their own and draw only what the
// product service reported about the session, so governance state is never part of the model's text. Every value is drawn as
// text. Nothing here publishes anything: Publish and Discard send a request that names the candidate shown, and the service decides.

import { Fragment, useEffect, useLayoutEffect, useRef } from "react";
import { Chip } from "../ui/Chip";
import { Disclosure } from "../ui/Disclosure";
import { Notice } from "../ui/Notice";
import { useAnnouncer } from "../ui/LiveRegion";
import { ActionNotice } from "./ActionNotice";
import type { Decision } from "./client";
import { COMPOSER_INPUT_ID } from "./LiveComposer";
import { useLive } from "./context";
import { budgetExhausted, budgetUsedUp, openTurnOf, phaseWords, remainingBudget } from "./readings";
import type { Candidate } from "./wire";
import styles from "./Live.module.css";

function turnLabel(id: string): string {
  return id.slice(id.lastIndexOf("/") + 1);
}

// A diff as plain text, one element per line. A line that starts with + or - is tinted, and keeps that character.
function DiffText({ path, diff }: { readonly path: string; readonly diff: string }) {
  const lines = diff.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  return (
    <pre className={styles.diff} tabIndex={0} aria-label={`Diff of ${path}`}>
      {lines.map((line, index) => (
        <span key={index} className={line.startsWith("+") ? styles.add : line.startsWith("-") ? styles.del : styles.ctx}>
          {line + "\n"}
        </span>
      ))}
    </pre>
  );
}

export const REVIEW_HEADING_ID = "live-review-heading";

function ReviewCard({ candidate }: { readonly candidate: Candidate }) {
  const live = useLive();
  const announcer = useAnnouncer();
  const { turnId, candidateDigest, changes } = candidate;
  const card = useRef<HTMLElement | null>(null);
  // The card leaves when the decision is recorded and the view is read again, and focus would leave with it. Focus moves to the
  // composer while the card still holds it, which is before its nodes are removed.
  useLayoutEffect(
    () => () => {
      if (card.current?.contains(document.activeElement)) document.getElementById(COMPOSER_INPUT_ID)?.focus();
    },
    [],
  );
  useEffect(() => {
    announcer.announce(`Review ready for ${turnLabel(turnId)}: ${changes.length} changed ${changes.length === 1 ? "path" : "paths"}.`);
  }, [announcer, turnId, candidateDigest, changes.length]);
  if (live === null) return null;
  const { controller, model } = live;
  const busy = model.action.status === "sending" || model.action.status === "unknown";
  const turn = model.view?.turns?.find((entry) => entry.id === candidate.turnId);

  async function decide(decision: Decision): Promise<void> {
    if (!busy) await controller.resolve(candidate, decision);
  }

  return (
    <section ref={card} className={styles.review} aria-label={`Review of ${turnLabel(candidate.turnId)}`} data-digest={candidate.candidateDigest}>
      <div className={styles.reviewHead}>
        <h3 id={REVIEW_HEADING_ID} tabIndex={-1} className={styles.reviewTitle}>
          {`Review changes · ${turnLabel(candidate.turnId)}`}
        </h3>
        <span className={styles.digest} id="live-candidate-digest" title={candidate.candidateDigest}>
          {`candidate ${candidate.candidateDigest}`}
        </span>
      </div>
      <p className={styles.hint}>
        {candidate.changes.length === 0
          ? "The candidate has no changed paths."
          : `${candidate.changes.length} changed ${candidate.changes.length === 1 ? "path" : "paths"}. Publish or Discard applies to exactly this candidate; if it changes, the service refuses and you see the new diff.`}
        {turn?.review_timeout_seconds ? ` Without a decision the review closes after ${turn.review_timeout_seconds} s.` : ""}
      </p>
      <div className={styles.files}>
        {candidate.changes.map((change) => (
          <Disclosure
            key={change.path}
            defaultOpen={candidate.changes.length <= 3}
            summary={
              <span className={styles.fileHead}>
                <Chip tone="idle">{change.op}</Chip>
                <span>{change.path}</span>
              </span>
            }
          >
            {change.diff === "" ? <p className={styles.hint}>No diff text.</p> : <DiffText path={change.path} diff={change.diff} />}
            {change.truncated ? <p className={styles.hint}>This diff was cut by the service.</p> : null}
          </Disclosure>
        ))}
      </div>
      <div className={styles.actions}>
        <button
          type="button"
          className={`${styles.button} ${styles.primary}`}
          aria-disabled={busy}
          aria-describedby="live-candidate-digest"
          onClick={() => void decide("publish")}
        >
          Publish
        </button>
        <button
          type="button"
          className={styles.button}
          aria-disabled={busy}
          aria-describedby="live-candidate-digest"
          onClick={() => void decide("discard")}
        >
          Discard
        </button>
      </div>
    </section>
  );
}

export function LivePanel() {
  const live = useLive();
  if (live === null) return null;
  const { model } = live;
  const view = model.view;
  if (view === null) return null;
  const left = remainingBudget(view);
  const open = openTurnOf(view);
  const unknownTurns = (view.turns ?? []).filter((turn) => turn.state === "unknown");
  const candidate = model.candidate;
  return (
    <div className={styles.panel} role="group" aria-label="Session state">
      <div className={styles.status} role="group" aria-label="Remaining budget">
        <Chip tone={budgetUsedUp(view) ? "deny" : "idle"}>{`host calls ${left.host_calls} left of ${view.budget.host_calls}`}</Chip>
        <Chip tone={budgetUsedUp(view) ? "deny" : "idle"}>{`tokens ${left.tokens} left of ${view.budget.tokens}`}</Chip>
        <Chip tone={budgetUsedUp(view) ? "deny" : "idle"}>{`seconds ${left.duration_seconds} left of ${view.budget.duration_seconds}`}</Chip>
        {budgetUsedUp(view) ? <Chip tone="deny">budget used up</Chip> : null}
        {budgetExhausted(left) && !budgetUsedUp(view) ? <Chip tone="idle">budget reserved by the running turn</Chip> : null}
        {view.read_only === true ? <Chip tone="idle">read-only session</Chip> : null}
        {open !== null ? (
          <Chip tone={open.phase === "unknown" ? "unknown" : "pending"}>{`${turnLabel(open.turn.id)} ${phaseWords(open.phase)}`}</Chip>
        ) : null}
        <span className={styles.muted}>{`revision ${view.revision}`}</span>
      </div>
      {view.blocked === true || unknownTurns.length > 0 ? (
        <Notice label="Session blocked" tone="warning">
          <p className={styles.noticeText}>
            {view.blocked === true
              ? "This session is blocked: sending is refused until an operator reconciles it."
              : "A turn has an unknown outcome. An operator must reconcile it."}
            {unknownTurns.length > 0 ? (
              <>
                {" "}
                Unknown outcome:{" "}
                {unknownTurns.map((turn, index) => (
                  <Fragment key={turn.id}>
                    {index > 0 ? ", " : ""}
                    {turnLabel(turn.id)}
                  </Fragment>
                ))}
                .
              </>
            ) : null}
          </p>
        </Notice>
      ) : null}
      {model.sinceLeft > 0 ? (
        <Notice label="Events while away" tone="info">
          <p className={styles.noticeText}>{`${model.sinceLeft} ${model.sinceLeft === 1 ? "event" : "events"} arrived while this page was closed. They are in the transcript.`}</p>
        </Notice>
      ) : null}
      <ActionNotice kinds={["resolve"]} label="Review decision request" />
      {candidate.status === "ready" ? (
        <div className={styles.noticeActions}>
          <button type="button" className={styles.button} onClick={() => document.getElementById(REVIEW_HEADING_ID)?.focus()}>
            Go to the review
          </button>
        </div>
      ) : null}
    </div>
  );
}

// The review of the candidate at the foot of the transcript, after the turns and apart from them. The candidate is read when
// the turn reaches review, and Publish and Discard are bound to the digest of the candidate that is shown.
export function LiveReview() {
  const live = useLive();
  if (live === null) return null;
  const { model, controller } = live;
  const candidate = model.candidate;
  if (candidate.status === "ready") return <ReviewCard candidate={candidate.candidate} />;
  if (candidate.status === "loading") return <p className={styles.hint}>Reading the candidate for review…</p>;
  if (candidate.status === "failed") {
    return (
      <Notice label="Candidate not loaded" tone="warning">
        <p className={styles.noticeText}>{`The candidate could not be read (${candidate.message}).`}</p>
        <div className={styles.noticeActions}>
          <button type="button" className={styles.button} onClick={() => void controller.loadCandidate()}>
            Read the candidate again
          </button>
        </div>
      </Notice>
    );
  }
  return null;
}

