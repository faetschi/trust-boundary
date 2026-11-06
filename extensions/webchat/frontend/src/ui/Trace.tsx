import { useState, type ReactNode } from "react";
import type { ChipTone } from "./Chip";
import styles from "./Trace.module.css";

// The trace pattern (the shape of shadcn AI Elements' Trace, written here): one step of agent work as a flat line with a rule at
// its left, a status icon, a title and a collapsible body. The tone is the state the record is in, never a running state: this
// client shows records, and a request with no result yet is not "running" (docs/design/design-language.md).

interface TraceProps {
  readonly tone: ChipTone;
  // The line: a TraceLine. Its content is the native summary.
  readonly line: ReactNode;
  readonly children: ReactNode;
  readonly defaultOpen?: boolean;
}

// A native <details>, controlled as Disclosure is: the browser gives Enter and Space on the summary, and React mirrors the
// open state so it survives a re-render.
export function Trace({ tone, line, children, defaultOpen = false }: TraceProps) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <details className={styles.trace} data-tone={tone} open={open} onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary className={styles.line}>{line}</summary>
      <div className={styles.body}>{children}</div>
    </details>
  );
}

// The line's content, wrapping: the icon, the title and its detail, then the chips.
export function TraceLine({ children }: { readonly children: ReactNode }) {
  return <span className={styles.lineContent}>{children}</span>;
}

// A tile of one or two characters in the tone's tint. Decorative: the words beside it carry the meaning.
export function TraceIcon({ children }: { readonly children: string }) {
  return (
    <span className={styles.icon} aria-hidden="true">
      {children}
    </span>
  );
}

interface TraceTitleProps {
  readonly title: string;
  readonly detail?: ReactNode;
}

// The step's target in bold, ending in an ellipsis; its full text is the title attribute. The detail line sits beneath it.
export function TraceTitle({ title, detail }: TraceTitleProps) {
  return (
    <span className={styles.titles}>
      <span className={styles.title} title={title}>
        {title}
      </span>
      {detail ? <span className={styles.detail}>{detail}</span> : null}
    </span>
  );
}

// The line's trailing chips, right-aligned, wrapping under the title on a narrow line.
export function TraceMeta({ children }: { readonly children: ReactNode }) {
  return <span className={styles.meta}>{children}</span>;
}
