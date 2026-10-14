// @vitest-environment happy-dom
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { act } from "react";
import { describe, expect, it } from "vitest";
import { createAnnouncer } from "./announcer";
import { AnnouncerContext, LiveRegion } from "./LiveRegion";
import { mount } from "../test-support/dom";

describe("LiveRegion: the single polite announcer", () => {
  it("renders one polite status region, and changes its text for every announcement", () => {
    const announcer = createAnnouncer();
    const view = mount(
      <AnnouncerContext.Provider value={announcer}>
        <LiveRegion />
      </AnnouncerContext.Provider>,
    );
    const region = view.container.querySelector("[role='status']");
    expect(region?.getAttribute("aria-live")).toBe("polite");
    expect(region?.getAttribute("aria-atomic")).toBe("true");
    act(() => announcer.announce("Stream connected."));
    const first = region?.textContent;
    expect(first?.trim()).toBe("Stream connected.");
    act(() => announcer.announce("Stream connected."));
    const second = region?.textContent;
    expect(second?.trim()).toBe("Stream connected.");
    expect(second).not.toBe(first);
    view.unmount();
  });

  it("is the only aria-live or live role in the application source, so announcements have one path", () => {
    // Run from the frontend directory (npm run test), so the source root is a plain path.
    const srcDir = resolve(process.cwd(), "src");
    const offenders: string[] = [];
    const walk = (dir: string) => {
      for (const entry of readdirSync(dir)) {
        const path = join(dir, entry);
        if (statSync(path).isDirectory()) {
          if (entry !== "test-support") walk(path);
          continue;
        }
        if (!/\.(tsx?|css)$/.test(entry) || /\.test\./.test(entry)) continue;
        const text = readFileSync(path, "utf8");
        // Attribute forms only: a comment that names aria-live is not markup.
        if (/aria-live=|role=["'{]\s*["']?(status|alert|log|marquee|timer)/.test(text)) {
          offenders.push(relative(srcDir, path).replace(/\\/g, "/"));
        }
      }
    };
    walk(srcDir);
    expect(offenders.sort()).toEqual(["ui/LiveRegion.tsx"]);
  });
});
