// @vitest-environment happy-dom
import { act } from "react";
import { expect, it } from "vitest";
import { resultItem } from "../../../core/testing/builders";
import type { ProjectionState } from "../../../core/verification";
import { mount } from "../../../test-support/dom";
import { SequenceRail } from "./SequenceRail";

it("updates verification and search wording with unchanged retained items", () => {
  const items = [resultItem({ id: "r1", sequence: 1, tool: "write", tool_call_id: "c1", state: "succeeded",
    authority: "observed", verification_scope: "structural-audit-only" })];
  const view = (projection: ProjectionState, searching = false) =>
    <SequenceRail items={items} phase="loaded" searching={searching} projection={projection} />;
  const mounted = mount(view({ source: "live", verification: "structural-audit-only" }));
  const toolsName = () => mounted.container.querySelector('[data-lane="tools"]')?.getAttribute("aria-label");
  const render = (projection: ProjectionState, searching = false) => act(() => mounted.root.render(view(projection, searching)));
  try {
    expect(toolsName()).toContain("succeeded, verified");
    render({ source: "fixture", verification: "structural-audit-only" });
    expect(toolsName()).toContain("succeeded, reported, not independently verified");
    render({ source: "live", verification: "structural-audit-only" });
    expect(toolsName()).toContain("succeeded, verified");
    render({ source: "live", verification: "unverified-input" });
    expect(toolsName()).toContain("succeeded, reported, not independently verified");
    render({ source: "live", verification: "structural-audit-only" }, true);
    expect(toolsName()).toContain("succeeded, verified");
    expect(mounted.container.textContent).toContain("search only narrows the ledger");
    render({ source: "live", verification: "structural-audit-only" });
    expect(mounted.container.textContent).not.toContain("search only narrows the ledger");
  } finally { mounted.unmount(); }
});
