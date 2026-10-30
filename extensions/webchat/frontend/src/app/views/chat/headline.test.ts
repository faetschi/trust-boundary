import { describe, expect, it } from "vitest";
import { headlineSummary } from "./headline";
import { EMPTY_TOOL_SUMMARY } from "./readings";

// The card headline is the tool name (drawn by the card) followed by the bounded summary with any
// leading repeat of the name removed. These cases are the fixture items and the edge cases.
describe("headlineSummary: the summary after the tool name", () => {
  it("drops the name the summary repeats, as in the fixture's 'bash: go test'", () => {
    expect(headlineSummary("bash", "bash: go test")).toBe("go test");
    expect(headlineSummary("bash", "bash: curl https://example.com")).toBe("curl https://example.com");
  });

  it("drops a repeated name more than once, so 'read read service/x' reads 'service/x'", () => {
    expect(headlineSummary("read", "read read service/x")).toBe("service/x");
  });

  it("keeps a summary that only starts with the name as a longer word", () => {
    expect(headlineSummary("read", "readme.md")).toBe("readme.md");
    expect(headlineSummary("bash", "bashful run")).toBe("bashful run");
  });

  it("keeps the name in the middle or the end of a summary", () => {
    expect(headlineSummary("read", "service/read")).toBe("service/read");
    expect(headlineSummary("read", "go read")).toBe("go read");
  });

  it("is empty when the summary is the tool name alone", () => {
    expect(headlineSummary("bash", "bash")).toBe("");
    expect(headlineSummary("bash", "  bash  ")).toBe("");
  });

  it("compares case-insensitively and keeps the summary's own case", () => {
    expect(headlineSummary("bash", "BASH: go test")).toBe("go test");
    expect(headlineSummary("Bash", "bash ls -la")).toBe("ls -la");
    expect(headlineSummary("bash", "Bash Run")).toBe("Run");
  });

  it("keeps a leading flag: a hyphen is not a separator", () => {
    expect(headlineSummary("bash", "bash -c ls")).toBe("-c ls");
  });

  it("handles unicode names and summaries, and a non-breaking space as a separator", () => {
    expect(headlineSummary("lesen", "lesen Datei")).toBe("Datei");
    expect(headlineSummary("ünïcode", "ünïcode: ok")).toBe("ok");
    expect(headlineSummary("ünïcode", "ünïcodex ok")).toBe("ünïcodex ok");
  });

  it("treats the name as text, not as a pattern", () => {
    expect(headlineSummary("a.b", "a.b c")).toBe("c");
    expect(headlineSummary("a+", "aaa")).toBe("aaa");
  });

  it("returns the summary unchanged when the tool name is empty", () => {
    expect(headlineSummary("", "go test")).toBe("go test");
  });

  it("says no bounded summary was projected for an empty or blank summary", () => {
    expect(headlineSummary("bash", undefined)).toBe(EMPTY_TOOL_SUMMARY);
    expect(headlineSummary("bash", "")).toBe(EMPTY_TOOL_SUMMARY);
    expect(headlineSummary("bash", "   ")).toBe(EMPTY_TOOL_SUMMARY);
  });

  it("does not strip the placeholder when the tool is named 'tool'", () => {
    expect(headlineSummary("tool", undefined)).toBe(EMPTY_TOOL_SUMMARY);
  });
});
