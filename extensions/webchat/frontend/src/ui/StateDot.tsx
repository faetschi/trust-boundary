import type { ChipTone } from "./Chip";
import styles from "./StateDot.module.css";

// A status mark. Its shape carries the state as well as its colour: ok is a solid circle,
// allow an inset (2 px) ring, deny a solid square, reported a dashed ring, unknown a dotted
// ring, pending a hollow ring, idle a solid grey dot, lost a solid diamond in the unknown colour
// (LOST is uncertain, not a refusal, so it is never the deny square).
//
// The dot is decorative. The element that owns it carries the text name (for example
// the row's aria-label), so a label is never announced twice. The owner must name the
// state in words.
export function StateDot({ tone }: { readonly tone: ChipTone }) {
  return <span className={styles.dot} data-tone={tone} aria-hidden="true" />;
}
