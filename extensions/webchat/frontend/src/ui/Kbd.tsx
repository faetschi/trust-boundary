import type { ReactNode } from "react";
import styles from "./Kbd.module.css";

export function Kbd({ children }: { readonly children: ReactNode }) {
  return <kbd className={styles.kbd}>{children}</kbd>;
}
