import type { ReactNode } from "react";
import styles from "./VisuallyHidden.module.css";

// Text that is read by assistive technology and not drawn.
export function VisuallyHidden({ children }: { readonly children: ReactNode }) {
  return <span className={styles.hidden}>{children}</span>;
}
