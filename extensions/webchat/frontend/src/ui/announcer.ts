// The announcer: one store of messages for the single polite live region (LiveRegion).
// It holds the latest message only. A burst of messages is the caller's to coalesce.
// This module reads no DOM and no timer.

export interface Announcement {
  // Increases on every announce, so a repeated message is still a new announcement.
  readonly serial: number;
  readonly text: string;
}

export interface Announcer {
  announce(text: string): void;
  subscribe(listener: () => void): () => void;
  getSnapshot(): Announcement;
}

export function createAnnouncer(): Announcer {
  let current: Announcement = { serial: 0, text: "" };
  const listeners = new Set<() => void>();
  return {
    announce(text: string) {
      if (!text) return;
      current = { serial: current.serial + 1, text };
      for (const listener of listeners) listener();
    },
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getSnapshot() {
      return current;
    },
  };
}

// The text the live region renders. A screen reader announces a change of text, so a
// repeated message would be silent. Every other announcement therefore ends in a
// no-break space, and consecutive identical messages always differ in text.
export function displayText(announcement: Announcement): string {
  if (!announcement.text) return "";
  return announcement.serial % 2 === 0 ? `${announcement.text} ` : announcement.text;
}
