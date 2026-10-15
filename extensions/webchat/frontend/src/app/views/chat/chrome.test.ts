// The message chrome (chrome.ts): the clock of a message, the role labels, and the Copy action. The clipboard is
// passed in, so the success and failure paths run without a browser.

import { describe, expect, it } from "vitest";
import {
  COPIED_LABEL,
  COPIED_MS,
  COPY_LABEL,
  COPY_TRUNCATED_LABEL,
  COPY_UNAVAILABLE_LABEL,
  clockHm,
  copyMessage,
  roleLabel,
} from "./chrome";

describe("roleLabel", () => {
  it("names the assistant Pi, the user User and a system record System", () => {
    expect(roleLabel("assistant")).toBe("Pi");
    expect(roleLabel("user")).toBe("User");
    expect(roleLabel("system")).toBe("System");
  });
});

describe("clockHm", () => {
  it("shows the local hour and minute as HH:mm, with no seconds", () => {
    const date = new Date("2026-10-13T09:12:04Z");
    const pad = (value: number) => String(value).padStart(2, "0");
    expect(clockHm("2026-10-13T09:12:04Z")).toBe(`${pad(date.getHours())}:${pad(date.getMinutes())}`);
  });

  it("reads an invalid time as unavailable, never as a clock", () => {
    expect(clockHm("not a time")).toBe("time unavailable");
  });
});

describe("copyMessage", () => {
  it("writes the projected text to the clipboard and resolves true", async () => {
    const written: string[] = [];
    const clipboard = {
      writeText: async (text: string) => {
        written.push(text);
      },
    };
    expect(await copyMessage("hello world", clipboard)).toBe(true);
    expect(written).toEqual(["hello world"]);
  });

  it("resolves false when the clipboard refuses, and never throws", async () => {
    const clipboard = {
      writeText: () => Promise.reject(new Error("permission denied")),
    };
    await expect(copyMessage("hello", clipboard)).resolves.toBe(false);
  });

  it("resolves false when the page has no clipboard, and never throws", async () => {
    await expect(copyMessage("hello", undefined)).resolves.toBe(false);
    await expect(copyMessage("hello", null)).resolves.toBe(false);
  });
});

describe("the Copy words", () => {
  it("are the plan's words, and the copied state lasts one second", () => {
    expect(COPY_LABEL).toBe("Copy message");
    expect(COPY_TRUNCATED_LABEL).toBe("Copy displayed text (truncated)");
    expect(COPIED_LABEL).toBe("Copied");
    expect(COPY_UNAVAILABLE_LABEL).toBe("Copy unavailable");
    expect(COPIED_MS).toBe(1000);
  });
});
