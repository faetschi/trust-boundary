// @vitest-environment happy-dom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it, vi } from "vitest";
import { decisionItem, messageItem, snapshotOf } from "../core/testing/builders";
import { Inspector } from "./Inspector";
import { DRAWER_BREAKPOINT_PX } from "./layout";
import { createWorkspaceStore, type WorkspaceModel } from "./workspaceStore";
import { click, mount } from "../test-support/dom";
import { fakeWorkspace, flush } from "../test-support/fakes";
import { createRef } from "react";

// A store with a loaded projection, so the model is what the shell would render.
async function modelWith(selectedId: string | null = null): Promise<WorkspaceModel> {
  const fake = fakeWorkspace(
    snapshotOf({
      items: [messageItem({ id: "m1", sequence: 1 }), decisionItem({ id: "d1", sequence: 2, tool: "bash", tool_call_id: "c1", verdict: "DENY" })],
    }),
  );
  const store = createWorkspaceStore(fake.deps);
  store.start();
  await flush();
  if (selectedId !== null) store.getSnapshot().actions.selectRecord(selectedId);
  return store.getSnapshot();
}

describe("Inspector: the drawer and the record slot", () => {
  it("is inert and hidden from assistive technology while the narrow drawer is closed", async () => {
    const model = await modelWith();
    const view = mount(<Inspector model={model} narrow open={false} headingRef={createRef()} onClose={vi.fn()} />);
    const aside = view.container.querySelector("aside");
    expect(aside?.hasAttribute("inert")).toBe(true);
    expect(aside?.getAttribute("aria-hidden")).toBe("true");
    expect(aside?.getAttribute("data-open")).toBe("false");
    view.unmount();
  });

  it("is reachable when open, and at wide widths whatever the drawer state", async () => {
    const model = await modelWith();
    const open = mount(<Inspector model={model} narrow open headingRef={createRef()} onClose={vi.fn()} />);
    expect(open.container.querySelector("aside")?.hasAttribute("inert")).toBe(false);
    open.unmount();
    const wide = mount(<Inspector model={model} narrow={false} open={false} headingRef={createRef()} onClose={vi.fn()} />);
    expect(wide.container.querySelector("aside")?.hasAttribute("inert")).toBe(false);
    wide.unmount();
  });

  it("shows the record slot for the selected record, with a close control that calls back", async () => {
    const model = await modelWith("d1");
    const onClose = vi.fn();
    const view = mount(<Inspector model={model} narrow={false} open={false} headingRef={createRef()} onClose={onClose} />);
    expect(view.container.querySelector("h2")?.textContent).toBe("Record");
    expect(view.container.querySelector("[data-section='decision']")).not.toBeNull();
    expect(view.container.textContent).toContain("sequence 2");
    click(view.container.querySelector("button") as HTMLButtonElement);
    expect(onClose).toHaveBeenCalledTimes(1);
    view.unmount();
  });

  it("moves focus to its heading when the focus ref is set by the shell", async () => {
    const model = await modelWith();
    const headingRef = createRef<HTMLHeadingElement>();
    const view = mount(<Inspector model={model} narrow open headingRef={headingRef} onClose={vi.fn()} />);
    headingRef.current?.focus();
    expect(document.activeElement).toBe(view.container.querySelector("h2"));
    view.unmount();
  });
});

describe("the drawer breakpoint is one value, in layout.ts and in the CSS", () => {
  it("the inspector and shell stylesheets use the same breakpoint as the hook", () => {
    expect(DRAWER_BREAKPOINT_PX).toBe(1180);
    for (const name of ["Inspector.module.css", "Shell.module.css"]) {
      const css = readFileSync(resolve(process.cwd(), "src/app", name), "utf8");
      expect(css).toContain(`@media (max-width: ${DRAWER_BREAKPOINT_PX}px)`);
    }
  });
});
