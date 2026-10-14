// @vitest-environment happy-dom
import { describe, expect, it } from "vitest";
import type { SourceClass, StreamStatus } from "../core/contract";
import { snapshotOf, streamDefault } from "../core/testing/builders";
import { mount } from "../test-support/dom";
import { fakeWorkspace, flush } from "../test-support/fakes";
import { Rail } from "./Rail";
import { createWorkspaceStore, type WorkspaceModel } from "./workspaceStore";

// A loaded model from one source with the given streams. The fake answers the fetch from the snapshot.
async function loadedFrom(source: SourceClass, streams: readonly StreamStatus[]): Promise<WorkspaceModel> {
  const fake = fakeWorkspace(
    snapshotOf({
      source,
      verification_scope: source === "live" ? "structural-audit-only" : "unverified-input",
      streams,
    }),
  );
  const store = createWorkspaceStore(fake.deps);
  store.start();
  await flush();
  return store.getSnapshot();
}

// The text of the stream row and the tone of its dot.
function rowOf(container: HTMLElement): { readonly text: string; readonly tone: string | null } {
  const row = container.querySelector("li");
  return { text: row?.textContent ?? "", tone: row?.querySelector("[data-tone]")?.getAttribute("data-tone") ?? null };
}

const RECEIPT = streamDefault({
  id: "receipt",
  label: "retained run receipt",
  health: "complete",
  reason: "terminal post-run receipt import",
});

describe("the rail's stream health: a finished stream is not a verified outcome", () => {
  // A stream that completes says only that it ended. The source did not claim completeness, so the row
  // is neutral in words and in its dot, and it never takes the success tone, from any source.
  for (const source of ["fixture", "replay", "live"] as const) {
    it(`reads a complete stream as complete (source did not claim completeness), on the neutral tone, from a ${source} projection`, async () => {
      const model = await loadedFrom(source, [RECEIPT]);
      const view = mount(<Rail model={model} />);
      const row = rowOf(view.container);
      expect(row.text).toContain("retained run receipt: complete (source did not claim completeness)");
      expect(row.tone).toBe("idle");
      expect(view.container.querySelector("[data-tone='ok']")).toBeNull();
      view.unmount();
    });
  }

  it("reads an unavailable stream in words, on the neutral tone, never the success tone", async () => {
    const model = await loadedFrom("fixture", [
      streamDefault({ id: "tap", label: "process tap", health: "unavailable", available: false }),
    ]);
    const view = mount(<Rail model={model} />);
    const row = rowOf(view.container);
    expect(row.text).toContain("process tap: unavailable, not available");
    expect(row.tone).toBe("idle");
    expect(view.container.querySelector("[data-tone='ok']")).toBeNull();
    view.unmount();
  });
});
