import type { ReactNode } from "react";
import styles from "./Notice.module.css";

type NoticeTone = "info" | "warning" | "claim";

interface NoticeProps {
  // The accessible name of the note.
  readonly label: string;
  readonly tone: NoticeTone;
  readonly children: ReactNode;
}

// A bounded note. It uses role="note", which is not a live region: announcements go
// through the single LiveRegion only, so no notice is announced twice or out of turn.
export function Notice({ label, tone, children }: NoticeProps) {
  return (
    <div role="note" aria-label={label} data-tone={tone} className={styles.notice}>
      {children}
    </div>
  );
}
