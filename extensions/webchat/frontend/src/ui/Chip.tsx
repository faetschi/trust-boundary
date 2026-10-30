import styles from "./Chip.module.css";

// The closed tone vocabulary shared with the status dot and the rail (core contract.ts).
export type ChipTone = "ok" | "allow" | "deny" | "reported" | "pending" | "unknown" | "idle" | "lost";

// The provenance roles of a record (core trajectory.ts RecordSource, without "Context").
export type SourceRole = "system" | "user" | "assistant" | "tool" | "decision" | "result" | "message";

type ChipProps = {
  // The text carries the meaning. A chip never relies on its colour or shape alone.
  readonly children: string;
} & (
  | { readonly tone: ChipTone; readonly source?: undefined }
  | { readonly source: SourceRole; readonly tone?: undefined }
);

// A state or source label. Colour comes from the state and source aliases through
// data attributes, so no class name depends on the variant.
export function Chip(props: ChipProps) {
  const { children } = props;
  if (props.source !== undefined) {
    return (
      <span className={styles.chip} data-source={props.source}>
        {children}
      </span>
    );
  }
  return (
    <span className={styles.chip} data-tone={props.tone}>
      {children}
    </span>
  );
}
