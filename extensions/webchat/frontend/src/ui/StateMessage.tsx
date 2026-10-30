import type { ReactNode } from "react";
import styles from "./StateMessage.module.css";

// Empty, error and loading states. None of them has a live role: announcements go
// through the single LiveRegion, and an error is announced there by the workspace.

interface StateMessageProps {
  readonly title: string;
  readonly children?: ReactNode;
}

export function EmptyState({ title, children }: StateMessageProps) {
  return (
    <div className={styles.state} data-state="empty">
      <p className={styles.title}>{title}</p>
      {children ? <div className={styles.body}>{children}</div> : null}
    </div>
  );
}

export function ErrorState({ title, children }: StateMessageProps) {
  return (
    <div className={styles.state} data-state="error">
      <p className={styles.title}>{title}</p>
      {children ? <div className={styles.body}>{children}</div> : null}
    </div>
  );
}

export function LoadingState({ label = "Loading…" }: { readonly label?: string }) {
  return (
    <div className={styles.state} data-state="loading" aria-busy="true">
      <p className={styles.body}>{label}</p>
    </div>
  );
}
