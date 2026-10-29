// The composer of the Chat view in live mode. A prompt is sent under a fresh request key against the revision the page last
// displayed. Send and Cancel stay in the tab order when they cannot act, drawn dashed with the reason beside them, so a
// keyboard or screen-reader user reads why instead of finding the control gone. The draft is kept while a turn runs, and
// cleared only when the service accepted the prompt: also when that happens through Retry, after an unknown outcome, so the
// same text is never left behind to be sent again under a new key.

import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { useAnnouncer } from "../ui/LiveRegion";
import { ActionNotice } from "./ActionNotice";
import { useLive } from "./context";
import { cancelGate, sendGate } from "./readings";
import styles from "./Live.module.css";

// Where focus returns when the control that held it leaves (the review card after a decision).
export const COMPOSER_INPUT_ID = "live-composer-input";
const REASON_ID = "live-composer-reason";
const IDLE_HINT = "Enter sends. Shift+Enter adds a line.";

export function LiveComposer() {
  const live = useLive();
  const announcer = useAnnouncer();
  const [draft, setDraft] = useState("");
  // The text of the prompt most recently handed to the controller, until the service confirms a prompt.
  const sent = useRef<string | null>(null);
  const done = live?.model.action.done;
  useEffect(() => {
    if (done?.kind !== "submit" || sent.current === null) return;
    const text = sent.current;
    sent.current = null;
    setDraft((current) => (current === text ? "" : current));
    announcer.announce("Prompt sent.");
  }, [done, announcer]);
  if (live === null) return null;
  const { model, controller } = live;
  const send = sendGate(model.view, model.action);
  const cancel = cancelGate(model.view, model.action);
  const ready = send.allowed && draft.trim() !== "";

  async function submit(): Promise<void> {
    if (!ready) return;
    sent.current = draft;
    await controller.submit(draft);
  }

  function onSubmit(event: FormEvent): void {
    event.preventDefault();
    void submit();
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>): void {
    if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    void submit();
  }

  return (
    <section aria-label="Message Pi" data-testid="composer" className={styles.composer}>
      <form className={styles.card} onSubmit={onSubmit}>
        <div className={styles.row}>
          <textarea
            id={COMPOSER_INPUT_ID}
            className={styles.prompt}
            aria-label="Message Pi"
            aria-describedby={REASON_ID}
            placeholder="Message Pi"
            rows={2}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={onKeyDown}
          />
          <button type="submit" className={`${styles.button} ${styles.primary}`} aria-disabled={!ready} onClick={(event) => !ready && event.preventDefault()}>
            Send
          </button>
          <button
            type="button"
            className={styles.button}
            aria-disabled={!cancel.allowed}
            title={cancel.reason ?? undefined}
            onClick={() => cancel.allowed && void controller.cancel()}
          >
            Cancel turn
          </button>
        </div>
        <p id={REASON_ID} className={styles.hint}>
          {send.reason ?? IDLE_HINT}
        </p>
        <ActionNotice kinds={["submit", "cancel"]} label="Prompt request" />
      </form>
    </section>
  );
}
