// @vitest-environment happy-dom
import { StrictMode } from "react";
import { act } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { messageItem, snapshotOf } from "../core/testing/builders";
import { mount, settle, type Mounted } from "../test-support/dom";
import { fakeWorkspace, type FakeWorkspace } from "../test-support/fakes";
import { useWorkspace, WorkspaceProvider } from "./WorkspaceProvider";

let mounted: Mounted | null = null;

afterEach(() => {
  mounted?.unmount();
  mounted = null;
});

function Probe() {
  const model = useWorkspace();
  return <p data-probe="connection">{model.status.label}</p>;
}

function probeText(): string | null | undefined {
  return mounted?.container.querySelector("[data-probe='connection']")?.textContent;
}

function snapshot() {
  return snapshotOf({ items: [messageItem({ id: "m1", sequence: 1 })] });
}

describe("WorkspaceProvider: lifecycle", () => {
  it("opens exactly one stream under StrictMode, which runs the effect twice", async () => {
    const fake: FakeWorkspace = fakeWorkspace(snapshot());
    mounted = mount(
      <StrictMode>
        <WorkspaceProvider deps={fake.deps}>
          <Probe />
        </WorkspaceProvider>
      </StrictMode>,
    );
    await settle();
    expect(fake.sources.filter((source) => !source.closed)).toHaveLength(1);
    act(() => fake.sources[fake.sources.length - 1]?.open());
    expect(probeText()).toBe("connected");
  });

  it("closes the stream when it unmounts, and makes no further request", async () => {
    const fake = fakeWorkspace(snapshot());
    mounted = mount(
      <WorkspaceProvider deps={fake.deps}>
        <Probe />
      </WorkspaceProvider>,
    );
    await settle();
    const source = fake.sources[0];
    expect(source?.closed).toBe(false);
    const requests = fake.fetchUrls.length;
    mounted.unmount();
    mounted = null;
    expect(source?.closed).toBe(true);
    expect(fake.fetchUrls.length).toBe(requests);
  });

  it("goes offline when the window does, and resumes when it comes back", async () => {
    const fake = fakeWorkspace(snapshot());
    mounted = mount(
      <WorkspaceProvider deps={fake.deps}>
        <Probe />
      </WorkspaceProvider>,
    );
    await settle();
    act(() => fake.sources[0]?.open());
    expect(probeText()).toBe("connected");
    fake.setOnline(false);
    act(() => window.dispatchEvent(new Event("offline")));
    expect(probeText()).toBe("offline");
    fake.setOnline(true);
    act(() => window.dispatchEvent(new Event("online")));
    await settle();
    expect(fake.fetchUrls.length).toBeGreaterThanOrEqual(2);
  });
});
