// The composer of the Chat view: a labelled, read-only region that submits nothing. It is not a form, it has no submit or
// key handler, and it makes no request. It has no button either: nothing here acts, so nothing here is drawn as a control to
// press. Its one behaviour is to say, politely, that this page cannot submit turns. The Pi version it shows is the
// projection's, or unavailable.

import { useState } from "react";
import type { SessionHeader } from "../core/contract";
import { Chip } from "../ui/Chip";
import { useAnnouncer } from "../ui/LiveRegion";
import { piVersionWord } from "./views/chat/identity";
import styles from "./Composer.module.css";

export const COMPOSER_LABEL = "Message Pi: not available in this build";
export const PLACEHOLDER = "Submitting turns is not available in this build.";
export const COMPOSER_REASON =
  "Read-only view of a Pi session. This page cannot submit turns, cancel, or approve. Turn submission is a later milestone.";
export const NOT_AVAILABLE_STATUS = "Not available: this page cannot submit turns.";
export const OBSERVED_CHIP = "Observed only · no approvals";
export const OBSERVED_TITLE = "This page observes the Pi session. It cannot approve, submit or cancel.";

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
        <div className={styles.info}>
          <p id="composer-reason" className={styles.reason}>
            {COMPOSER_REASON}
          </p>
          <div className={styles.chips}>
            <span title={OBSERVED_TITLE}>
              <Chip tone="idle">{OBSERVED_CHIP}</Chip>
            </span>
            <Chip tone="idle">{`Pi ${piVersionWord(session)} · tools unavailable`}</Chip>
          </div>
        </div>
        <p id="composer-status" className={styles.status}>
          {status}
        </p>
      </div>
    </section>
  );
}
