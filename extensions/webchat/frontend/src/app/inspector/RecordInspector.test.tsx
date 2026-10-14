// @vitest-environment happy-dom
// The record inspector body, rendered on its own. The shell's aside, heading and Close control are
// tested with the shell (app/views/TrajectoryView.test.tsx).
import { afterEach, describe, expect, it } from "vitest";
import { activityItem, decisionItem, messageItem, resultItem } from "../../core/testing/builders";
import { mount, type Mounted } from "../../test-support/dom";
import { RecordInspector } from "./RecordInspector";

const DIGEST = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

let mounted: Mounted | null = null;

// The projection the record is shown in. Live with a structural audit by default, so an observed and
// audited record is verified; the fixture cases pass FIXTURE.
const LIVE = { source: "live", verification: "structural-audit-only" } as const;
const FIXTURE = { source: "fixture", verification: "unverified-input" } as const;

function inspect(
  record: Parameters<typeof RecordInspector>[0]["record"],
  projection: Parameters<typeof RecordInspector>[0]["projection"] = LIVE,
): HTMLElement {
  mounted = mount(<RecordInspector record={record} projection={projection} />);
  return mounted.container;
}

function section(container: HTMLElement, id: string): HTMLElement {
  const match = container.querySelector<HTMLElement>(`[data-section='${id}']`);
  if (!match) throw new Error(`no section ${id}`);
  return match;
}

// The value cell of the row with this label, in the section.
function valueOf(container: HTMLElement, sectionId: string, label: string): HTMLElement {
  const term = Array.from(section(container, sectionId).querySelectorAll("dt")).find((dt) => dt.textContent === label);
  const cell = term?.nextElementSibling;
  if (!(cell instanceof HTMLElement)) throw new Error(`no ${label} in ${sectionId}`);
  return cell;
}

afterEach(() => {
  mounted?.unmount();
  mounted = null;
});

describe("the record inspector: header and sections", () => {
  it("names the source and the position in its header", () => {
    const view = inspect(decisionItem({ id: "d1", sequence: 3, turn_id: "t1", tool: "bash", tool_call_id: "c1", verdict: "ALLOW" }));
    expect(view.querySelector("span")?.textContent).toBe("Decision");
    expect(view.textContent).toContain("sequence 3 · turn t1");
  });

  it("shows a Summary, a Decision, an Evidence and a Timing section for a decision", () => {
    const view = inspect(decisionItem({ id: "d1", sequence: 3, tool: "bash", tool_call_id: "c1", verdict: "DENY", reason_code: "policy_rule_deny" }));
    for (const id of ["summary", "decision", "evidence", "timing"]) {
      expect(view.querySelector(`[data-section='${id}']`), id).not.toBeNull();
    }
    expect(view.querySelector("[data-section='result']")).toBeNull();
    expect(valueOf(view, "decision", "reason code").textContent).toBe("policy_rule_deny");
    expect(valueOf(view, "decision", "verdict").textContent).toBe("DENY");
  });

  it("shows the Result section, with the state and the lossy row, for a result", () => {
    const view = inspect(resultItem({ id: "r1", sequence: 4, tool: "bash", tool_call_id: "c1", state: "succeeded", lossy: true, digest: DIGEST }));
    expect(section(view, "result").textContent).toContain("lossy");
    expect(valueOf(view, "result", "lossy").textContent).toMatch(/yes — output completeness is not a terminal outcome/);
  });
});

describe("the Decision section: a shortened digest with the full value in its title", () => {
  it("shortens a policy digest and carries the full value in the title", () => {
    const view = inspect(decisionItem({ id: "d1", sequence: 3, tool: "bash", tool_call_id: "c1", verdict: "ALLOW", policy_digest: DIGEST }));
    const cell = valueOf(view, "decision", "policy digest");
    const shown = cell.querySelector("[title]");
    expect(shown?.getAttribute("title")).toBe(DIGEST);
    expect(shown?.textContent).toContain("…");
    expect(shown?.textContent).not.toContain(DIGEST);
  });

  it("reads an absent digest as the dash, not as an empty value", () => {
    const view = inspect(decisionItem({ id: "d1", sequence: 3, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" }));
    expect(valueOf(view, "decision", "policy digest").textContent).toBe("—");
  });
});

describe("the Result section: an unverified success is reported, never confirmed", () => {
  it("shows an unverified success as reported", () => {
    const view = inspect(resultItem({ id: "r1", sequence: 4, tool: "bash", tool_call_id: "c1", state: "succeeded" }));
    expect(valueOf(view, "result", "state").textContent).toBe("reported");
    expect(view.querySelectorAll("[data-tone='ok']")).toHaveLength(0);
  });

  it("shows a lossy success as succeeded (output lossy, not confirmed), on the unknown tone", () => {
    const view = inspect(
      resultItem({ id: "r1", sequence: 4, tool: "bash", tool_call_id: "c1", state: "succeeded", lossy: true, authority: "observed", verification_scope: "structural-audit-only" }),
    );
    expect(valueOf(view, "result", "state").textContent).toBe("succeeded (output lossy, not confirmed)");
    expect(valueOf(view, "result", "state").querySelector("[data-tone]")?.getAttribute("data-tone")).toBe("unknown");
  });

  it("shows a verified success as succeeded, on the ok tone", () => {
    const view = inspect(
      resultItem({ id: "r1", sequence: 4, tool: "bash", tool_call_id: "c1", state: "succeeded", authority: "observed", verification_scope: "structural-audit-only" }),
    );
    expect(valueOf(view, "result", "state").querySelector("[data-tone]")?.getAttribute("data-tone")).toBe("ok");
    expect(valueOf(view, "result", "state").textContent).toBe("succeeded");
  });

  it("shows a lost result as lost, not as a success", () => {
    const view = inspect(resultItem({ id: "r1", sequence: 4, tool: "bash", tool_call_id: "c1", state: "lost" }));
    expect(valueOf(view, "result", "state").textContent).toBe("lost");
    expect(valueOf(view, "result", "state").querySelector("[data-tone]")?.getAttribute("data-tone")).toBe("lost");
  });
});

describe("the Evidence section and the Timing section", () => {
  it("says independently verified: no unless the projection verified the record", () => {
    const unverified = inspect(decisionItem({ id: "d1", sequence: 3, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" }));
    expect(valueOf(unverified, "evidence", "independently verified").textContent).toBe("no");
    mounted?.unmount();
    const verified = inspect(
      decisionItem({ id: "d2", sequence: 4, tool: "bash", tool_call_id: "c2", verdict: "ALLOW", authority: "observed", verification_scope: "structural-audit-only" }),
    );
    expect(valueOf(verified, "evidence", "independently verified").textContent).toBe("yes");
  });

  it("in a fixture projection, a streamed observed success reads reported and independently verified: no, and keeps its own evidence", () => {
    const view = inspect(
      resultItem({ id: "r1", sequence: 4, tool: "bash", tool_call_id: "c1", state: "succeeded", authority: "observed", verification_scope: "structural-audit-only" }),
      FIXTURE,
    );
    expect(valueOf(view, "result", "state").textContent).toBe("reported");
    expect(valueOf(view, "evidence", "independently verified").textContent).toBe("no");
    expect(valueOf(view, "evidence", "authority").textContent).toBe("observed");
    expect(valueOf(view, "evidence", "verification").textContent).toBe("structural-audit-only");
  });

  it("reads absent authority and verification as unavailable, never as no or unknown", () => {
    const bare: Record<string, unknown> = { ...decisionItem({ id: "d1", sequence: 3, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" }) };
    delete bare.authority;
    delete bare.verification_scope;
    const view = inspect(bare as unknown as Parameters<typeof RecordInspector>[0]["record"]);
    expect(valueOf(view, "evidence", "independently verified").textContent).toBe("unavailable — the record carries no authority or verification scope");
    expect(valueOf(view, "evidence", "authority").textContent).toBe("unavailable");
    expect(valueOf(view, "evidence", "verification").textContent).toBe("unavailable");
  });

  it("always has a Timing section, which states that no duration is recorded", () => {
    for (const item of [
      messageItem({ id: "m1", sequence: 1 }),
      activityItem({ id: "a1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "pending" }),
      decisionItem({ id: "d1", sequence: 3, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" }),
      resultItem({ id: "r1", sequence: 4, tool: "bash", tool_call_id: "c1", state: "succeeded" }),
    ]) {
      mounted?.unmount();
      const view = inspect(item);
      expect(view.querySelector("[data-section='timing']"), item.id).not.toBeNull();
      expect(valueOf(view, "timing", "duration").textContent).toBe("unavailable — the projection records no duration");
      expect(section(view, "timing").textContent).toContain("the projection records a timestamp, not a duration");
    }
  });

  it("shows the recorded timestamp as a timestamp, not as a duration", () => {
    const view = inspect(messageItem({ id: "m1", sequence: 1, occurred_at: "2026-10-13T09:12:04Z" }));
    expect(valueOf(view, "timing", "recorded timestamp").textContent).toBe("2026-10-13T09:12:04Z");
    expect(valueOf(view, "timing", "recorded at").textContent).toMatch(/a clock reading, not a duration/);
  });
});

describe("the record inspector draws nothing it was not given", () => {
  it("renders no inline style, so the CSP holds", () => {
    const view = inspect(resultItem({ id: "r1", sequence: 4, tool: "bash", tool_call_id: "c1", state: "denied", digest: DIGEST, lossy: true }));
    expect(view.querySelectorAll("[style]")).toHaveLength(0);
  });
});
