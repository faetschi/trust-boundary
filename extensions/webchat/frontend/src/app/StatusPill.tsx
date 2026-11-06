// The status pill: the one place the header says what this page is looking at. Its words name the worst reading (a demonstration,
// an unverified source, a connection that is not open, a claim the projection makes that nothing verifies), and it opens the
// full reading: the source, the session, the connection and the claim boundary. Nothing the old header showed is dropped; it
// is one click away, and the pill's words and its dot never say less than the worst of it.

import { useEffect, useId, useRef, useState } from "react";
import { StateDot } from "../ui/StateDot";
import type { ChipTone } from "../ui/Chip";
import { isVerifiedProjection } from "../core/format";
import { ClaimBoundary, claimsUnverifiedTrue } from "./ClaimBoundary";
import type { ConnectionStatus } from "./status";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Shell.module.css";

const NO_SESSION_TEXT = "no session selected";

function sourceText(model: WorkspaceModel): string {
  const scope = isVerifiedProjection(model) ? "" : " · unverified";
  return `source: ${model.source}${scope}`;
}

function connectionText(status: ConnectionStatus): string {
  return status.warning ? `${status.label} · ${status.warning}` : status.label;
}

const isDemonstration = (model: WorkspaceModel): boolean => model.source === "fixture" || model.source === "replay";

// The pill's words: the demonstration or the source first, then the claim boundary's worst reading (an unverified true, or the
// containment of a source that is not a demonstration), then anything about the connection that is not as expected. The claim words
// come before the connection, so a cut or a wrap never takes the claim away. A quiet, healthy, verified view reads as its source alone.
export function pillWords(model: WorkspaceModel, noSession: boolean): string {
  const parts = [isDemonstration(model) ? "Demonstration · unverified" : sourceText(model)];
  if (claimsUnverifiedTrue(model)) parts.push("unverified claim true");
  else if (!isDemonstration(model)) {
    // A source that is not a demonstration still says where the claim boundary stands: its containment, in words.
    const containment = model.panels?.qualification?.containment;
    parts.push(containment === undefined ? "claim boundary unavailable" : containment === "unavailable" ? "containment unavailable" : "containment not established");
  }
  if (noSession) parts.push(NO_SESSION_TEXT);
  else if (model.status.state !== "open") parts.push(connectionText(model.status));
  return parts.join(" · ");
}

// The dot's shape and colour follow the worst reading. A stream that failed or is offline, and a claim that nothing verifies, are
// "unknown": the page cannot say, and it is not a policy refusal, so it never takes the refusal square. Everything else is the dashed
// "reported" ring: a demonstration, an unverified view, a connecting view, and a source whose containment is not established (the
// contract has no established containment today, so no view is drawn quiet).
function pillTone(model: WorkspaceModel, noSession: boolean): ChipTone {
  if (!noSession && (model.status.state === "error" || model.status.state === "offline")) return "unknown";
  return claimsUnverifiedTrue(model) ? "unknown" : "reported";
}
interface StatusPillProps {
  readonly model: WorkspaceModel;
  readonly noSession: boolean;
}

export function StatusPill({ model, noSession }: StatusPillProps) {
  const [open, setOpen] = useState(false);
  const popoverId = useId();
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const buttonRef = useRef<HTMLButtonElement | null>(null);

  // A press outside closes it. Escape closes it and returns focus to the pill; it stops there, so the shell's own Escape (the
  // record, the drawer) is left for the next press.
  useEffect(() => {
    if (!open) return undefined;
    function onPointerDown(event: PointerEvent) {
      if (event.target instanceof Node && !wrapRef.current?.contains(event.target)) setOpen(false);
    }
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open]);

  return (
    <div
      ref={wrapRef}
      className={styles.statusWrap}
      // Focus that leaves the pill and its popover closes it, so Escape is never left for the shell to read as "clear the record".
      onBlur={(event) => {
        if (open && !(event.relatedTarget instanceof Node && wrapRef.current?.contains(event.relatedTarget))) setOpen(false);
      }}
      onKeyDown={(event) => {
        if (event.key !== "Escape" || !open) return;
        event.stopPropagation();
        setOpen(false);
        buttonRef.current?.focus();
      }}
    >
      <button
        ref={buttonRef}
        type="button"
        className={styles.pill}
        aria-expanded={open}
        aria-controls={popoverId}
        onClick={() => setOpen((value) => !value)}
      >
        <StateDot tone={pillTone(model, noSession)} />
        <span className={styles.pillText}>{pillWords(model, noSession)}</span>
      </button>
      {/* The popover is in the page while it is closed (hidden), so its values are never absent from the document, only
          from view. */}
      <div id={popoverId} className={styles.popover} hidden={!open} tabIndex={-1}>
        <ul className={styles.sourceList} aria-label="Source and connection">
          <li>
            <span className={styles.badge} data-source={model.source}>
              {sourceText(model)}
            </span>
          </li>
          {isDemonstration(model) ? (
            <li>
              <span className={styles.badge} data-demonstration="">
                demonstration · not a live session
              </span>
            </li>
          ) : null}
          <li>
            <span className={styles.badge} data-connection={noSession ? "idle" : model.status.state}>
              {noSession ? NO_SESSION_TEXT : connectionText(model.status)}
            </span>
          </li>
        </ul>
        <ClaimBoundary model={model} />
      </div>
    </div>
  );
}
