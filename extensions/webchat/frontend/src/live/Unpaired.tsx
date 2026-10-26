import { Notice } from "../ui/Notice";
import styles from "./Live.module.css";

// A live gateway answers 401 until the browser is paired. The pairing URL carries a one-time token in its fragment, and only
// the terminal that started the gateway has it, so this page can only say where to find it.
export function Unpaired() {
  return (
    <main className={styles.page} aria-label="Pairing required">
      <h1 className={styles.pageTitle}>tbound web chat</h1>
      <Notice label="Pairing required" tone="warning">
        <p className={styles.noticeText}>
          This is a live gateway, and this browser is not paired with it. Open the pairing URL that tbound-webchat printed in its
          terminal. A pairing token works once and for 10 minutes. If it has expired, restart tbound-webchat for a new one.
        </p>
      </Notice>
    </main>
  );
}
