// The composer of the Chat view: a labelled, read-only region that submits nothing. It is not a form, it has no submit or
// key handler, and it makes no request. It is drawn as the quiet T3-style card (a field, then one row of what this session is and a
// round send mark), and the mark is a grey, aria-disabled button that says, politely, that this page cannot submit turns. Nothing
// else here is a control, and the two facts in the row are text, not pickers: a caret on a control that does nothing would be a
// fake affordance. The Pi version it shows is the projection's, or unavailable.

import { useState } from "react";
import type { SessionHeader } from "../core/contract";
import { LockIcon, SendIcon } from "../ui/Icon";
import { VisuallyHidden } from "../ui/VisuallyHidden";
import { useAnnouncer } from "../ui/LiveRegion";
import { piVersionWord } from "./views/chat/identity";
import styles from "./Composer.module.css";

const COMPOSER_LABEL = "Message Pi: not available in this build";
const PLACEHOLDER = "Submitting turns is not available in this build.";
const COMPOSER_REASON =
  "Read-only view of a Pi session. This page cannot submit turns, cancel, or approve. Turn submission is a later milestone.";
const NOT_AVAILABLE_STATUS = "Not available: this page cannot submit turns.";
const OBSERVED_CHIP = "Observed only · no approvals";
const OBSERVED_TITLE = "This page observes the Pi session. It cannot approve, submit or cancel.";

export const SEND_LABEL = "Send: not available in this build";

export function Composer({ session }: { readonly session: SessionHeader | null }) {
  // The explanation is spoken by the shell's one polite announcer (LiveRegion), so the app keeps a single live path. The
  // same sentence is drawn in a plain line, so a sighted reader sees what was announced. The line moves no focus.
  const announcer = useAnnouncer();
  const [status, setStatus] = useState("");
  const explain = () => {
    setStatus(NOT_AVAILABLE_STATUS);
    announcer.announce(NOT_AVAILABLE_STATUS);
  };
  return (
    <section aria-label={COMPOSER_LABEL} data-testid="composer" className={styles.composer}>
      <div className={styles.card}>
        <textarea
          aria-label="Message Pi"
          aria-describedby="composer-reason"
          aria-disabled="true"
          aria-readonly="true"
          className={styles.input}
          onClick={explain}
          onFocus={explain}
          placeholder={PLACEHOLDER}
          readOnly
          rows={1}
        />
        {/* The full reason is read by assistive technology with the field, and it is in the details card's read-only section. */}
        <VisuallyHidden>
          <span id="composer-reason">{COMPOSER_REASON}</span>
        </VisuallyHidden>
        <div className={styles.controls}>
          <span className={styles.fact} data-fact="" title={OBSERVED_TITLE}>
            <LockIcon />
            {OBSERVED_CHIP}
          </span>
          <span className={styles.fact} data-fact="">{`Pi ${piVersionWord(session)} · tools unavailable`}</span>
          <button type="button" className={styles.send} aria-disabled="true" aria-label={SEND_LABEL} onClick={explain}>
            <SendIcon />
          </button>
        </div>
        <p id="composer-status" className={styles.status}>
          {status}
        </p>
      </div>
    </section>
  );
}