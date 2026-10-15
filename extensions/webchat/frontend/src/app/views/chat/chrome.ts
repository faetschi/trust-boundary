// The message chrome of the Chat view (Transcript.tsx): the clock of a message, the role labels, and the Copy action and
// its words. Pure, except copyMessage, which takes the clipboard it is given and reads no global, so the success and the
// failure paths run without a browser.

import type { Role } from "../../../core/contract";

export const COPY_LABEL = "Copy message";
export const COPY_TRUNCATED_LABEL = "Copy displayed text (truncated)";
export const COPIED_LABEL = "Copied";
export const COPY_UNAVAILABLE_LABEL = "Copy unavailable";
// How long "Copied" stays before the label returns to Copy message.
export const COPIED_MS = 1000;
export const COPIED_ANNOUNCEMENT = "Message copied.";
export const COPY_UNAVAILABLE_ANNOUNCEMENT = "Copy unavailable.";

// The role as the transcript names it: the assistant is Pi, and the user is User.
export function roleLabel(role: Role): string {
  switch (role) {
    case "assistant":
      return "Pi";
    case "user":
      return "User";
    default:
      return "System";
  }
}

// The accessible name of a Copy control: the visible label first, so the words on screen are in the name, then the message
// it copies. The speaker and the record number tell the controls apart. The record is unique in the transcript; a time is not.
export function copyName(label: string, role: Role, sequence: number): string {
  return `${label}: ${copyTargetOf(role, sequence)}`;
}

function copyTargetOf(role: Role, sequence: number): string {
  switch (role) {
    case "assistant":
      return `Pi, record ${sequence}`;
    case "user":
      return `your message, record ${sequence}`;
    default:
      return `system note, record ${sequence}`;
  }
}

// The clock of a message as HH:mm, in local time, with no seconds. An invalid time reads unavailable.
export function clockHm(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "time unavailable";
  const pad = (part: number) => String(part).padStart(2, "0");
  return `${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

export interface ClipboardLike {
  writeText(text: string): Promise<void>;
}

// Copies the projected text. Resolves true when the clipboard took it, and false when the page has no clipboard or the
// browser refused. It never throws.
export async function copyMessage(text: string, clipboard: ClipboardLike | null | undefined): Promise<boolean> {
  if (!clipboard || typeof clipboard.writeText !== "function") return false;
  try {
    await clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
