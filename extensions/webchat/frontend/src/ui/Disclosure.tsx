import { useState, type ReactNode } from "react";
import styles from "./Disclosure.module.css";

interface DisclosureProps {
  readonly summary: ReactNode;
  readonly children: ReactNode;
  readonly defaultOpen?: boolean;
  readonly onToggle?: (open: boolean) => void;
}

// A native <details> disclosure. The browser provides the keyboard behaviour (Enter and
// Space on the summary) and the expanded state. The open state is mirrored in React so that
// the element stays controlled across renders.
export function Disclosure({ summary, children, defaultOpen = false, onToggle }: DisclosureProps) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <details
      className={styles.disclosure}
      open={open}
      onToggle={(event) => {
        const next = event.currentTarget.open;
        setOpen(next);
        onToggle?.(next);
      }}
    >
      <summary className={styles.summary}>{summary}</summary>
      <div className={styles.body}>{children}</div>
    </details>
  );
}
