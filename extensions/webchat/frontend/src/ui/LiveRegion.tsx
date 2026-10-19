import { createContext, useContext, useSyncExternalStore } from "react";
import { displayText, type Announcer } from "./announcer";
import styles from "./LiveRegion.module.css";

// The announcer the workspace provides. LiveRegion reads it; nothing else renders a
// live region, so there is exactly one polite announcer on the page.
export const AnnouncerContext = createContext<Announcer | null>(null);

export function useAnnouncer(): Announcer {
  const announcer = useContext(AnnouncerContext);
  if (!announcer) throw new Error("useAnnouncer: no AnnouncerContext provider");
  return announcer;
}

// The single polite live region. Its text is visually hidden, and it is the only element
// in the app with an aria-live or status role.
export function LiveRegion() {
  const announcer = useAnnouncer();
  const announcement = useSyncExternalStore(announcer.subscribe, announcer.getSnapshot, announcer.getSnapshot);
  return (
    <div role="status" aria-live="polite" aria-atomic="true" className={styles.region}>
      {displayText(announcement)}
    </div>
  );
}
