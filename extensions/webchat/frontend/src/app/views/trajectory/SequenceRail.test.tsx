// @vitest-environment happy-dom
import { act } from "react";
import { expect, it } from "vitest";
import { decisionItem, messageItem, resultItem } from "../../../core/testing/builders";
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

it("keys its colours and shapes in words, listing only the states the retained records show", () => {
  const items = [
    messageItem({ id: "m1", sequence: 1, role: "user", text: "hi" }),
    decisionItem({ id: "d1", sequence: 2, tool: "bash", tool_call_id: "c1", verdict: "DENY" }),
    resultItem({ id: "r1", sequence: 3, tool: "bash", tool_call_id: "c1", state: "denied" }),
  ];
  const mounted = mount(<SequenceRail items={items} phase="loaded" searching={false} projection={{ source: "fixture", verification: "unverified-input" }} />);
  const key = mounted.container.querySelector("[aria-label='Key to the rail']") as HTMLElement;
  expect(key.textContent).toContain("Model: messages (user, assistant, system)");
  expect(key.textContent).toContain("Policy: policy decisions");
  expect(key.textContent).toContain("Tools: tool requests and results");
  expect(key.textContent).toContain("DENY or denied");
  expect(key.textContent).toContain("no record of this kind here");
  expect(key.textContent).not.toContain("LOST");
  expect(key.querySelectorAll("[aria-hidden='true']").length).toBeGreaterThan(3);
  mounted.unmount();
});