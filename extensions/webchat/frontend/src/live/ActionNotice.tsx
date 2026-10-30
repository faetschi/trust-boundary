import { useEffect } from "react";
import { Notice } from "../ui/Notice";
import { useAnnouncer } from "../ui/LiveRegion";
import type { IntentKind } from "./client";
import { useLive } from "./context";
import { actionText, retryable } from "./readings";
import styles from "./Live.module.css";

// The state of the mutation in flight when it is one of `kinds`: a repeat under way, a refusal, an unknown outcome, or a
// replay. A refusal that would only be refused again offers Dismiss alone. A failure that may not have run, or an
// unknown outcome, offers Retry, which sends the same request key. The text is spoken once through the shell's announcer.
export function ActionNotice({ kinds, label }: { readonly kinds: readonly IntentKind[]; readonly label: string }) {
  const live = useLive();
  const announcer = useAnnouncer();
  const action = live?.model.action;
  const kind = action?.intent?.kind ?? action?.done?.kind;
  const text = action !== undefined && kind !== undefined && kinds.includes(kind) ? actionText(action) : null;
  useEffect(() => {
    if (text !== null) announcer.announce(text);
  }, [text, announcer]);
  if (live === null || action === undefined || text === null) return null;
  const settled = action.status === "failed" || action.status === "unknown";
  return (
    <Notice label={label} tone="warning">
      <p className={styles.noticeText} data-testid="action-notice">
        {text}
      </p>
      {settled ? (
        <div className={styles.noticeActions}>
          {action.status === "unknown" || (action.error !== null && retryable(action.error)) ? (
            <button type="button" className={styles.button} onClick={() => void live.controller.retry()}>
              Retry with the same request key
            </button>
          ) : null}
          {action.status === "failed" ? <button type="button" className={styles.button} onClick={() => live.controller.dismiss()}>
            Dismiss
          </button> : null}
        </div>
      ) : null}
    </Notice>
  );
}
