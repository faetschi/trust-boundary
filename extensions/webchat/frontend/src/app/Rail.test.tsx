// @vitest-environment happy-dom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it, vi } from "vitest";
import type { SessionHeader, SourceClass, StreamStatus } from "../core/contract";
import { snapshotOf, streamDefault } from "../core/testing/builders";
import { click, mount } from "../test-support/dom";
import { fakeWorkspace, flush } from "../test-support/fakes";
import { Rail, RailToggle } from "./Rail";
import { PHONE_BREAKPOINT_PX } from "./layout";
import { createWorkspaceStore, type WorkspaceModel } from "./workspaceStore";

// A loaded model from one source with the given streams and session. The fake answers the fetch from the snapshot.
async function loadedFrom(
  source: SourceClass,
  streams: readonly StreamStatus[] = [],
  session: SessionHeader | null = DEMO_SESSION,
): Promise<WorkspaceModel> {
  const fake = fakeWorkspace(
    snapshotOf({
      source,
      verification_scope: source === "live" ? "structural-audit-only" : "unverified-input",
      streams,
      session: session ?? undefined,
    }),
  );
  const store = createWorkspaceStore(fake.deps);
  store.start();
  await flush();
  return store.getSnapshot();
}

const DEMO_SESSION: SessionHeader = {
  session_id: "session-demo-01",
  title: "Demo session",
  status: "completed",
  turn_count: 1,
  pi_version: "0.87.1",
};

const NOTE =
  "Other Pi sessions are not listed. This page receives one session from the supervisor. A session list needs the supervisor session service, which is not available in this build.";

// The desktop collapse toggle on its own: the shell draws it in the header's brand row (Shell.test.tsx checks that).
function renderToggle(expanded: boolean, onToggle: () => void = () => undefined) {
  return mount(<RailToggle expanded={expanded} onToggle={onToggle} />);
}

function renderRail(model: WorkspaceModel, props: { expanded?: boolean; phone?: boolean; onToggle?: () => void } = {}) {
  return mount(
    <Rail
      model={model}
      phone={props.phone ?? false}
      expanded={props.expanded ?? true}
      onToggle={props.onToggle ?? (() => undefined)}
    />,
  );
}

// The text of the stream row and the tone of its dot.
function streamRow(container: HTMLElement): { readonly text: string; readonly tone: string | null } {
  const row = container.querySelector("[aria-labelledby='rail-streams'] li");
  return { text: row?.textContent ?? "", tone: row?.querySelector("[data-tone]")?.getAttribute("data-tone") ?? null };
}

function sessionRows(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>("ul[aria-label='Sessions'] > li"));
}

const RECEIPT = streamDefault({
  id: "receipt",
  label: "retained run receipt",
  health: "complete",
  reason: "terminal post-run receipt import",
});

describe("the sessions rail: one current session, honestly labelled", () => {
  it("is headed Sessions and lists exactly one session row, marked current and not interactive", async () => {
    const view = renderRail(await loadedFrom("fixture"));
    // The source binding is the rail's first section (v2 order), so the Sessions heading is found by its id.
    expect(view.container.querySelector("#rail-sessions-title")?.textContent).toBe("Sessions");
    const rows = sessionRows(view.container);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.getAttribute("aria-current")).toBe("true");
    expect(rows[0]?.querySelector("button, a, input, select, textarea")).toBeNull();
    view.unmount();
  });

  it("names the session in words: title, Pi version, turns, and a status that is reported, not verified", async () => {
    const view = renderRail(await loadedFrom("fixture"));
    const text = sessionRows(view.container)[0]?.textContent ?? "";
    expect(text).toContain("Demo session");
    expect(text).toContain("Pi 0.87.1 · 1 turn");
    expect(text).toContain("completed · reported, not verified");
    view.unmount();
  });

  it("states that other sessions are not listed, in these exact words, as a note", async () => {
    const view = renderRail(await loadedFrom("fixture"));
    expect(view.container.querySelector("[role='note']")?.textContent).toBe(NOTE);
    view.unmount();
  });

  it("offers no new-session control, no plus control, and no fake session rows", async () => {
    const view = renderRail(await loadedFrom("fixture"));
    expect(view.container.textContent).not.toMatch(/new session/i);
    const labels = Array.from(view.container.querySelectorAll("button")).map((b) => (b.textContent ?? "").trim());
    expect(labels).not.toContain("+");
    view.unmount();
  });

  it("reads an absent title or version as unavailable, and an absent turn count as unavailable, never as zero", async () => {
    const bare = { session_id: "s-1", status: "completed" } as SessionHeader;
    const view = renderRail(await loadedFrom("fixture", [], bare));
    const text = sessionRows(view.container)[0]?.textContent ?? "";
    expect(text).toContain("Untitled session unavailable");
    expect(text).toContain("Pi version unavailable · turns unavailable");
    expect(text).not.toMatch(/\b0 turns?\b/);
    view.unmount();
  });

  // Absent is unavailable, a stated zero is 0 turns, and a stated count is its number.
  it.each([
    [undefined, "turns unavailable"],
    [0, "0 turns"],
    [1, "1 turn"],
    [3, "3 turns"],
  ] as const)("reads turn_count %s as '%s'", async (turns, text) => {
    const session = { session_id: "s-1", status: "completed", title: "T", pi_version: "0.87.1", turn_count: turns } as SessionHeader;
    const view = renderRail(await loadedFrom("fixture", [], session));
    expect(sessionRows(view.container)[0]?.textContent ?? "").toContain(`Pi 0.87.1 · ${text}`);
    view.unmount();
  });

  it("never reads a completed status as verified, even from a live projection", async () => {
    const view = renderRail(await loadedFrom("live"));
    const text = sessionRows(view.container)[0]?.textContent ?? "";
    expect(text).toContain("reported, not verified");
    expect(text).not.toContain("verified by the gateway");
    view.unmount();
  });

  it("has no control and no link or field: the desktop collapse toggle is in the header's brand row", async () => {
    const view = renderRail(await loadedFrom("fixture"));
    const nav = view.container.querySelector("nav") as HTMLElement;
    expect(nav.querySelectorAll("a, input, select, textarea")).toHaveLength(0);
    expect(nav.querySelectorAll("button")).toHaveLength(0);
    view.unmount();
  });

  it("reads before a projection as no session projection, with the same note", () => {
    const store = createWorkspaceStore(fakeWorkspace(snapshotOf()).deps);
    const view = renderRail(store.getSnapshot());
    expect(view.container.textContent).toContain("No session projection");
    expect(view.container.querySelector("[role='note']")?.textContent).toBe(NOTE);
    view.unmount();
  });
});

describe("the rail collapses and expands", () => {
  it("announces the toggle's state, and names the action it takes", async () => {
    const open = renderToggle(true);
    const toggle = open.container.querySelector("button") as HTMLButtonElement;
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(toggle.getAttribute("aria-controls")).toBe("rail-body");
    expect(toggle.getAttribute("aria-label")).toBe("Collapse sessions panel");
    open.unmount();
    const closed = renderToggle(false);
    const collapsedToggle = closed.container.querySelector("button") as HTMLButtonElement;
    expect(collapsedToggle.getAttribute("aria-expanded")).toBe("false");
    expect(collapsedToggle.getAttribute("aria-label")).toBe("Expand sessions panel");
    closed.unmount();
  });

  it("calls back once when the toggle is pressed", async () => {
    const onToggle = vi.fn();
    const view = renderToggle(true, onToggle);
    click(view.container.querySelector("button") as HTMLButtonElement);
    expect(onToggle).toHaveBeenCalledTimes(1);
    view.unmount();
  });

  it("collapsed, has an empty hidden body, so nothing in it can take focus, and a dot that names the status", async () => {
    const view = renderRail(await loadedFrom("fixture"), { expanded: false });
    const body = view.container.querySelector("#rail-body") as HTMLElement;
    expect(body.hasAttribute("hidden")).toBe(true);
    expect(body.children).toHaveLength(0);
    expect(view.container.querySelector("[role='img']")?.getAttribute("aria-label")).toBe("Session: completed, reported");
    expect(view.container.querySelectorAll("button")).toHaveLength(0);
    view.unmount();
  });

  it("on a phone, is a disclosure strip that names the session, and is collapsed by default", async () => {
    const model = await loadedFrom("fixture");
    const view = renderRail(model, { phone: true, expanded: false });
    const strip = view.container.querySelector("h2 button") as HTMLButtonElement;
    expect(strip.textContent?.replace(/\s+/g, " ").trim()).toBe("Sessions · Demo session ▾");
    expect(strip.getAttribute("aria-expanded")).toBe("false");
    expect((view.container.querySelector("#rail-body") as HTMLElement).children).toHaveLength(0);
    view.unmount();
    const open = renderRail(model, { phone: true, expanded: true });
    const expandedStrip = open.container.querySelector("h2 button") as HTMLButtonElement;
    expect(expandedStrip.getAttribute("aria-expanded")).toBe("true");
    expect(sessionRows(open.container)).toHaveLength(1);
    open.unmount();
  });
});

describe("the rail's stream health: a finished stream is not a verified outcome", () => {
  // A stream that completes says only that it ended. The source did not claim completeness, so the row
  // is neutral in words and in its dot, and it never takes the success tone, from any source.
  for (const source of ["fixture", "replay", "live"] as const) {
    it(`reads a complete stream as complete (source did not claim completeness), on the neutral tone, from a ${source} projection`, async () => {
      const view = renderRail(await loadedFrom(source, [RECEIPT]));
      const row = streamRow(view.container);
      expect(row.text).toContain("retained run receipt: complete (source did not claim completeness)");
      expect(row.tone).toBe("idle");
      expect(view.container.querySelector("[data-tone='ok']")).toBeNull();
      view.unmount();
    });
  }

  it("reads an unavailable stream in words, on the neutral tone, never the success tone", async () => {
    const view = renderRail(
      await loadedFrom("fixture", [streamDefault({ id: "tap", label: "process tap", health: "unavailable", available: false })]),
    );
    const row = streamRow(view.container);
    expect(row.text).toContain("process tap: unavailable, not available");
    expect(row.tone).toBe("idle");
    expect(view.container.querySelector("[data-tone='ok']")).toBeNull();
    view.unmount();
  });
});

describe("the rail's layout is one set of breakpoints and motion rules", () => {
  const css = readFileSync(resolve(process.cwd(), "src/app/Rail.module.css"), "utf8");

  it("stacks the rail at the phone breakpoint, the value layout.ts names", () => {
    expect(PHONE_BREAKPOINT_PX).toBe(760);
    expect(css).toContain(`@media (max-width: ${PHONE_BREAKPOINT_PX}px)`);
  });

  it("runs the rail's transition only where motion is allowed", () => {
    expect(css).toMatch(/@media \(prefers-reduced-motion: no-preference\) \{\s*\.rail \{\s*transition:/);
  });

  it("defines the collapsed rail width as a token, 3.5rem", () => {
    const tokens = readFileSync(resolve(process.cwd(), "src/styles/tokens.css"), "utf8");
    expect(tokens).toMatch(/--rail-collapsed-width:\s*3\.5rem;/);
  });

  // The session's status row: the dot is set on the baseline of the first line, and only the words wrap beneath it, so
  // the dot is never pushed onto a line of its own (it was, at the 210 px rail).
  it("keeps the session status dot on the first line, beside the words", () => {
    const rule = /(?:^|[\s},])\.statusLine\s*\{([^}]*)\}/.exec(css.replace(/\/\*[\s\S]*?\*\//g, ""))?.[1] ?? "";
    expect(rule).toMatch(/display:\s*flex;/);
    expect(rule).toMatch(/align-items:\s*baseline;/);
    expect(rule).not.toMatch(/flex-wrap:\s*wrap;/);
  });
});
