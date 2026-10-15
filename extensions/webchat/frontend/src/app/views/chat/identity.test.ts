// The conversation head of the Chat view (identity.ts). Pure: every value is the projection's, an absent value reads
// "unavailable", and no session status is read as verified, because a session header carries no evidence.

import { describe, expect, it } from "vitest";
import { SESSION_STATUSES, type SessionHeader, type SessionStatus } from "../../../core/contract";
import { sessionDefault } from "../../../core/testing/builders";
import {
  NO_PI_VERSION,
  NO_STATUS,
  NO_TITLE,
  REPORTED_NOT_VERIFIED,
  conversationHead,
  identityLine,
  piVersionWord,
} from "./identity";

function sessionOf(overrides: Partial<SessionHeader>): SessionHeader {
  return {
    ...sessionDefault(),
    title: "Demo session",
    pi_version: "0.87.1",
    session_id: "session-demo-01",
    workflow_id: "wf-demo-01",
    status: "completed",
    ...overrides,
  };
}

describe("the conversation head", () => {
  it("names the session and its Pi version, session id and workflow id, all from the projection", () => {
    const head = conversationHead(sessionOf({}), true);
    expect(head.title).toBe("Demo session");
    expect(head.piVersion).toBe("0.87.1");
    expect(identityLine(head)).toBe("Pi 0.87.1 · session-demo-01 · wf-demo-01");
  });

  it("states the status word together with the reading reported, not verified", () => {
    expect(conversationHead(sessionOf({ status: "completed" }), true).status).toBe("completed · reported, not verified");
  });

  it("reads each absent value as unavailable, never as a blank", () => {
    const head = conversationHead(
      sessionOf({
        title: undefined,
        pi_version: undefined,
        session_id: undefined,
        workflow_id: undefined,
        status: undefined as unknown as SessionStatus,
      }),
      true,
    );
    expect(head.title).toBe(NO_TITLE);
    expect(identityLine(head)).toBe("Pi version unavailable · session id unavailable · workflow id unavailable");
    expect(head.status).toBe(NO_STATUS);
  });

  it("before the first snapshot shows the Pi session heading, with every value unavailable", () => {
    const head = conversationHead(null, false);
    expect(head.title).toBe("Pi session");
    expect(identityLine(head)).toBe("Pi version unavailable · session id unavailable · workflow id unavailable");
    expect(head.status).toBe(NO_STATUS);
  });

  it("never reads any session status as verified", () => {
    for (const status of SESSION_STATUSES) {
      const line = conversationHead(sessionOf({ status }), true).status;
      expect(line).toContain(REPORTED_NOT_VERIFIED);
      expect(line).not.toMatch(/(^|· )verified/);
    }
  });
});

describe("piVersionWord", () => {
  it("is the projected Pi version, or version unavailable when the projection carries none", () => {
    expect(piVersionWord(sessionOf({ pi_version: "0.87.1" }))).toBe("0.87.1");
    expect(piVersionWord(sessionOf({ pi_version: undefined }))).toBe(NO_PI_VERSION);
    expect(piVersionWord(null)).toBe(NO_PI_VERSION);
  });
});
