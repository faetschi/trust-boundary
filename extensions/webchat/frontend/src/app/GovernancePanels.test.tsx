// @vitest-environment happy-dom
// Ports the trust-label half of the legacy render.test.mjs case "unverified panel and session claims carry an
// explicit textual trust label" (lines 128-129): the Publication and Recovery panels render the sentence in the
// DOM, not only in the panel model that panels.test.ts checks.
import { describe, expect, it } from "vitest";
import { panelsDefault, snapshotOf } from "../core/testing/builders";
import { mount } from "../test-support/dom";
import { fakeWorkspace, flush } from "../test-support/fakes";
import { GovernancePanels } from "./GovernancePanels";
import { UNREPORTED_TRUST } from "./panels";
import { createWorkspaceStore } from "./workspaceStore";

const SENTENCE = "reported by the source, not independently verified";

async function unverifiedModel() {
  const panels = { ...panelsDefault(), publication: { state: "published" as const }, recovery: { state: "ready" as const } };
  const store = createWorkspaceStore(fakeWorkspace(snapshotOf({ panels })).deps);
  store.start();
  await flush();
  return store.getSnapshot();
}

describe("governance panels under an unverified projection", () => {
  it("pins the sentence the legacy client rendered", () => {
    expect(UNREPORTED_TRUST).toBe(SENTENCE);
  });

  for (const [panel, title] of [["publication", "Publication"], ["recovery", "Recovery"]] as const) {
    it(`the ${title} panel renders the reported-not-verified sentence in the DOM`, async () => {
      const view = mount(<GovernancePanels model={await unverifiedModel()} panel={panel} />);
      const text = view.container.textContent ?? "";
      expect(text).toContain(title);
      expect(text).toContain(SENTENCE);
      expect(text).not.toContain("verified by the gateway");
      view.unmount();
    });
  }
});
