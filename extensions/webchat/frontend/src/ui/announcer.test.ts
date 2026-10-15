import { describe, expect, it } from "vitest";
import { createAnnouncer, displayText } from "./announcer";

describe("announcer", () => {
  it("holds the latest message and notifies subscribers", () => {
    const announcer = createAnnouncer();
    let calls = 0;
    const unsubscribe = announcer.subscribe(() => {
      calls += 1;
    });
    announcer.announce("Stream connected.");
    expect(announcer.getSnapshot().text).toBe("Stream connected.");
    expect(calls).toBe(1);
    unsubscribe();
    announcer.announce("Stream offline.");
    expect(calls).toBe(1);
  });

  it("ignores an empty message", () => {
    const announcer = createAnnouncer();
    const before = announcer.getSnapshot();
    announcer.announce("");
    expect(announcer.getSnapshot()).toBe(before);
  });

  it("makes a repeated message a change of text, so a screen reader speaks it again", () => {
    const announcer = createAnnouncer();
    announcer.announce("Stream reconnecting.");
    const first = displayText(announcer.getSnapshot());
    announcer.announce("Stream reconnecting.");
    const second = displayText(announcer.getSnapshot());
    announcer.announce("Stream reconnecting.");
    const third = displayText(announcer.getSnapshot());
    expect(first).not.toBe(second);
    expect(second).not.toBe(third);
    expect(first.trim()).toBe("Stream reconnecting.");
    expect(third.trim()).toBe("Stream reconnecting.");
  });

  it("renders nothing before any message", () => {
    expect(displayText(createAnnouncer().getSnapshot())).toBe("");
  });
});
