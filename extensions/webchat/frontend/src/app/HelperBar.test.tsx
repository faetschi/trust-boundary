// @vitest-environment happy-dom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import type { SessionHeader, SourceClass, StreamStatus } from "../core/contract";
import { snapshotOf, streamDefault } from "../core/testing/builders";
import { mount } from "../test-support/dom";
import { fakeWorkspace, flush } from "../test-support/fakes";
import { HelperBar } from "./HelperBar";
import { createWorkspaceStore, type WorkspaceModel } from "./workspaceStore";

const SESSION: SessionHeader = { session_id: "s-1", title: "Demo", status: "completed", turn_count: 1, pi_version: "0.87.1" };

const NOTE =
  "Other Pi sessions are not listed. This page receives one session from the supervisor. A session list needs the supervisor session service, which is not available in this build.";

async function loadedFrom(source: SourceClass, streams: readonly StreamStatus[] = []): Promise<WorkspaceModel> {
  const store = createWorkspaceStore(
    fakeWorkspace(snapshotOf({ source, verification_scope: source === "live" ? "structural-audit-only" : "unverified-input", streams, session: SESSION })).deps,
  );
  store.start();
  await flush();
  return store.getSnapshot();
}

function show(model: WorkspaceModel, open = true) {
  return mount(<HelperBar model={model} open={open} version="0.1.0" />);
}

const RECEIPT = streamDefault({ id: "receipt", label: "retained run receipt", health: "complete", reason: "terminal post-run receipt import" });

describe("the details card", () => {
  it("is in the page and hidden while closed, so what it holds is never absent from the document", async () => {
    const view = show(await loadedFrom("fixture"), false);
    const card = view.container.querySelector("section") as HTMLElement;
    expect(card.hidden).toBe(true);
    expect(card.textContent).toContain(NOTE);
    view.unmount();
  });

  it("states the one-session note in its exact words, as a note, and the build version", async () => {
    const view = show(await loadedFrom("fixture"));
    expect(view.container.querySelector("[role='note']")?.textContent).toBe(NOTE);
    expect(view.container.querySelector("[aria-label='Session statistics']")?.textContent).toContain("build 0.1.0");
    view.unmount();
  });

  it("says the worst fact of a closed section on its own line", async () => {
    const view = show(await loadedFrom("fixture", [streamDefault({ id: "tap", label: "process tap", health: "gap", available: false })]));
    const leads = Array.from(view.container.querySelectorAll("summary")).map((summary) => summary.textContent ?? "");
    expect(leads.some((line) => line.startsWith("Observation streams") && line.includes("1 unavailable or gapped"))).toBe(true);
    expect(leads.some((line) => line.startsWith("Session") && line.includes("completed · reported, not verified"))).toBe(true);
    view.unmount();
  });

  it("reads before a projection as unavailable, with the same note", () => {
    const store = createWorkspaceStore(fakeWorkspace(snapshotOf()).deps);
    const view = show(store.getSnapshot());
    expect(view.container.textContent).toContain("unavailable (no projection loaded)");
    expect(view.container.textContent).not.toContain("No streams projected");
    view.unmount();
  });
});

describe("the details card behind a modal drawer, and its closed lines", () => {
  it("is inert while the drawer is open, as the header, the rail and the workspace are", async () => {
    const model = await loadedFrom("fixture");
    const view = mount(<HelperBar model={model} open version="0.1.0" inert />);
    expect((view.container.querySelector("section") as HTMLElement).hasAttribute("inert")).toBe(true);
    view.unmount();
  });

  it("lets a closed line wrap instead of cutting it with an ellipsis", () => {
    const css = readFileSync(resolve(process.cwd(), "src/app/HelperBar.module.css"), "utf8");
    const lead = /\.lead\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
    expect(lead).not.toMatch(/text-overflow:\s*ellipsis/);
    expect(lead).not.toMatch(/white-space:\s*nowrap/);
  });
});

describe("the details card's stream health: a finished stream is not a verified outcome", () => {
  // A stream that completes says only that it ended. The source did not claim completeness, so the row
  // is neutral in words and in its dot, and it never takes the success tone, from any source.
  for (const source of ["fixture", "replay", "live"] as const) {
    it(`reads a complete stream as complete (source did not claim completeness), on the neutral tone, from a ${source} projection`, async () => {
      const view = show(await loadedFrom(source, [RECEIPT]));
      const row = view.container.querySelector("ul li") as HTMLElement;
      expect(row.textContent).toContain("retained run receipt: complete (source did not claim completeness)");
      expect(row.querySelector("[data-tone]")?.getAttribute("data-tone")).toBe("idle");
      expect(view.container.querySelector("[data-tone='ok']")).toBeNull();
      view.unmount();
    });
  }

  it("reads an unavailable stream in words, on the neutral tone, never the success tone", async () => {
    const view = show(await loadedFrom("fixture", [streamDefault({ id: "tap", label: "process tap", health: "unavailable", available: false })]));
    const row = view.container.querySelector("ul li") as HTMLElement;
    expect(row.textContent).toContain("process tap: unavailable, not available");
    expect(row.querySelector("[data-tone]")?.getAttribute("data-tone")).toBe("idle");
    view.unmount();
  });
});
