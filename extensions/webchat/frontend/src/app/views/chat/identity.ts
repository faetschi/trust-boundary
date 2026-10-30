// The conversation head of the Chat view, and the Pi identity words the composer shares. Pure: no DOM, no React.
// Every value is the projection's. An absent value reads "unavailable", never a default, never zero, never blank.
// A session header carries no evidence, so its status is always read as reported, not verified.

import type { SessionHeader } from "../../../core/contract";
import { humanize } from "../../../core/format";

export const NO_PI_VERSION = "version unavailable";
export const NO_TITLE = "Session title unavailable";
export const NO_STATUS = "status unavailable";
export const REPORTED_NOT_VERIFIED = "reported, not verified";

const NO_SESSION_ID = "session id unavailable";
const NO_WORKFLOW_ID = "workflow id unavailable";

export interface ConversationHead {
  readonly title: string;
  // Each identity value on its own: the projected value, or its unavailable words. The view draws the Pi version in its
  // own element, so its text is exactly the projected value.
  readonly piVersion: string;
  readonly sessionId: string;
  readonly workflowId: string;
  // "<status word> · reported, not verified", or "status unavailable".
  readonly status: string;
}

function present(value: string | null | undefined): string | null {
  return typeof value === "string" && value.trim() !== "" ? value : null;
}

// The Pi version as a word: the projected version, or "version unavailable".
export function piVersionWord(session: SessionHeader | null | undefined): string {
  return present(session?.pi_version) ?? NO_PI_VERSION;
}

// The identity line as text: "Pi <version> · <session id> · <workflow id>".
export function identityLine(head: Pick<ConversationHead, "piVersion" | "sessionId" | "workflowId">): string {
  return [`Pi ${head.piVersion}`, head.sessionId, head.workflowId].join(" · ");
}

// The head of the conversation. Before the first snapshot it names the Pi session, and every value is unavailable.
export function conversationHead(session: SessionHeader | null, loaded: boolean): ConversationHead {
  if (!loaded || session === null) {
    return {
      title: "Pi session",
      piVersion: NO_PI_VERSION,
      sessionId: NO_SESSION_ID,
      workflowId: NO_WORKFLOW_ID,
      status: NO_STATUS,
    };
  }
  const status = present(session.status);
  return {
    title: present(session.title) ?? NO_TITLE,
    piVersion: piVersionWord(session),
    sessionId: present(session.session_id) ?? NO_SESSION_ID,
    workflowId: present(session.workflow_id) ?? NO_WORKFLOW_ID,
    status: status === null ? NO_STATUS : `${humanize(status)} · ${REPORTED_NOT_VERIFIED}`,
  };
}
