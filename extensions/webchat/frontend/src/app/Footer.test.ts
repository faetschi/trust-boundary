import { describe, expect, it } from "vitest";
import { decisionItem, messageItem } from "../core/testing/builders";
import { sessionDefault } from "../core/testing/builders";
import { footerMetrics } from "./Footer";
import { NO_PROJECTION, type Reading } from "./panels";

function valueOf(metrics: ReturnType<typeof footerMetrics>, label: string): Reading {
  const found = metrics.find(([name]) => name === label);
  if (!found) throw new Error(`no metric ${label}`);
  return found[1];
}

describe("footer metrics", () => {
  it("counts the turns of the session, and the retained records", () => {
    const metrics = footerMetrics({
      session: { ...sessionDefault(), turn_count: 3 },
      items: [messageItem({ id: "m1", sequence: 1 })],
      evicted: 0,
      loaded: true,
    });
    expect(valueOf(metrics, "Turns")).toEqual({ kind: "value", text: "3" });
    expect(valueOf(metrics, "Records retained")).toEqual({ kind: "value", text: "1" });
  });

  it("marks the turns unavailable, with its reason, when there is no session", () => {
    const metrics = footerMetrics({ session: null, items: [], evicted: 0, loaded: true });
    expect(valueOf(metrics, "Turns")).toEqual({ kind: "unavailable", reason: "no session projection" });
  });

  it("reads every retained-window count as unavailable before a projection is loaded, never as zero", () => {
    const metrics = footerMetrics({ session: null, items: [], evicted: 0, loaded: false });
    expect(valueOf(metrics, "Records retained")).toEqual({ kind: "unavailable", reason: NO_PROJECTION });
    expect(valueOf(metrics, "Policy, retained window")).toEqual({ kind: "unavailable", reason: NO_PROJECTION });
    expect(valueOf(metrics, "Turns")).toEqual({ kind: "unavailable", reason: "no session projection" });
  });

  it("reads an empty loaded window as zero, because the projection did load and retained nothing", () => {
    const metrics = footerMetrics({ session: null, items: [], evicted: 0, loaded: true });
    expect(valueOf(metrics, "Records retained")).toEqual({ kind: "value", text: "0" });
    expect(valueOf(metrics, "Policy, retained window")).toEqual({ kind: "value", text: "ALLOW 0 · DENY 0 · UNKNOWN 0" });
  });

  it("marks the metrics the projection does not record as unavailable, never as zero", () => {
    const metrics = footerMetrics({ session: null, items: [], evicted: 0, loaded: true });
    expect(valueOf(metrics, "Token usage")).toEqual({
      kind: "unavailable",
      reason: "the projection records no token usage",
    });
    expect(valueOf(metrics, "Duration")).toEqual({ kind: "unavailable", reason: "the projection records no duration" });
  });

  it("counts the policy verdicts of the retained window, and notes the evicted earlier records", () => {
    const metrics = footerMetrics({
      session: null,
      items: [
        decisionItem({ id: "d1", sequence: 1, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" }),
        decisionItem({ id: "d2", sequence: 2, tool: "bash", tool_call_id: "c2", verdict: "DENY" }),
        decisionItem({ id: "d3", sequence: 3, tool: "bash", tool_call_id: "c3", verdict: "UNKNOWN" }),
      ],
      evicted: 4,
      loaded: true,
    });
    expect(valueOf(metrics, "Policy, retained window")).toEqual({
      kind: "value",
      text: "ALLOW 1 · DENY 1 · UNKNOWN 1",
    });
    expect(valueOf(metrics, "Records retained")).toEqual({ kind: "value", text: "3 · 4 earlier evicted" });
  });
});
