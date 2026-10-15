import { describe, expect, it } from "vitest";
import type { Containment, GovernancePanel, Panels, PublicationPanel } from "../../core/contract";
import { decisionItem, panelsDefault } from "../../core/testing/builders";
import { PANEL_KEYS, type PanelKey } from "../uiPrefs";
import { panelBadge, warningBadge, type BadgeSource, type PanelBadge } from "./panelBadge";

function source(overrides: Partial<BadgeSource> = {}): BadgeSource {
  return {
    loaded: true,
    panels: panelsDefault(),
    source: "fixture",
    verification: "unverified-input",
    warnings: [],
    ...overrides,
  };
}

function withPanels(overrides: Partial<Panels>, sourceOverrides: Partial<BadgeSource> = {}): BadgeSource {
  return source({ panels: { ...panelsDefault(), ...overrides }, ...sourceOverrides });
}

const UNAVAILABLE: PanelBadge = { text: "unavailable", tone: "neutral" };
const VERIFIED = { source: "live", verification: "structural-audit-only" } as const;

describe("panel badges: words, and never the success tone", () => {
  it("never returns the success tone, for any panel, state or projection", () => {
    const states = [
      "succeeded",
      "completed",
      "published",
      "ready",
      "failed",
      "quarantined",
      "pending",
      "unknown",
      "lost",
      "not-attempted",
      "not-required",
      "recovery-required",
      "something-new",
    ];
    const projections = [source(), source(VERIFIED), source({ loaded: false, panels: null })];
    for (const base of projections) {
      for (const key of PANEL_KEYS) {
        for (const state of states) {
          const panels: Panels = {
            ...panelsDefault(),
            publication: { state } as PublicationPanel,
            recovery: { state } as Panels["recovery"],
          };
          const badge = panelBadge(key, { ...base, panels });
          expect(["neutral", "warn", "danger"], `${key} ${state}`).toContain(badge.tone);
          expect(badge.text.length, `${key} ${state}`).toBeGreaterThan(0);
        }
      }
    }
  });

  it("gives every panel a visible word, so the badge never relies on colour alone", () => {
    for (const key of PANEL_KEYS) expect(panelBadge(key, source()).text).not.toBe("");
  });
});

describe("the decisions badge", () => {
  it("shows a denial first, as a danger verdict", () => {
    expect(panelBadge("decisions", withPanels({ governance: { allow_count: 1, deny_count: 1, unknown_count: 0 } }))).toEqual({
      text: "1 deny verdict",
      tone: "danger",
    });
  });

  it("shows a denial above an unknown when both are stated, so the danger verdict wins", () => {
    expect(panelBadge("decisions", withPanels({ governance: { allow_count: 0, deny_count: 1, unknown_count: 2 } }))).toEqual({
      text: "1 deny verdict",
      tone: "danger",
    });
  });

  it("shows an unknown count next, as a warning verdict, plural for two", () => {
    expect(panelBadge("decisions", withPanels({ governance: { allow_count: 3, deny_count: 0, unknown_count: 2 } }))).toEqual({
      text: "2 unknown verdicts",
      tone: "warn",
    });
  });

  it("reads allow-only with its count, as a neutral word, never as success", () => {
    expect(panelBadge("decisions", withPanels({ governance: { allow_count: 4, deny_count: 0, unknown_count: 0 } }))).toEqual({
      text: "4 allow verdicts only",
      tone: "neutral",
    });
  });

  it("reads zero verdicts with a policy digest as no decisions, not as allow", () => {
    const governance = { allow_count: 0, deny_count: 0, unknown_count: 0, policy_digest: "sha256:ab" };
    expect(panelBadge("decisions", withPanels({ governance }))).toEqual({ text: "no decisions", tone: "neutral" });
  });

  it("reads stated zero counts as no decisions, with or without a policy digest", () => {
    expect(panelBadge("decisions", source())).toEqual({ text: "no decisions", tone: "neutral" });
  });

  it("reads zero counts as unavailable when a loaded decision carries a verdict, since the counts contradict it", () => {
    const governance = { allow_count: 0, deny_count: 0, unknown_count: 0, policy_digest: "sha256:ab" };
    const verdict = decisionItem({ id: "d1", sequence: 1, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" });
    expect(panelBadge("decisions", withPanels({ governance }, { items: [verdict] }))).toEqual(UNAVAILABLE);
  });

  it("reads an absent governance panel, or a count the panel does not carry, as unavailable", () => {
    expect(panelBadge("decisions", withPanels({ governance: null as unknown as GovernancePanel }))).toEqual(UNAVAILABLE);
    const partial = { allow_count: "3", deny_count: 0, unknown_count: 0 } as unknown as GovernancePanel;
    expect(panelBadge("decisions", withPanels({ governance: partial }))).toEqual(UNAVAILABLE);
  });

  it("reads unavailable before a projection is loaded, even when a stale panel is present", () => {
    expect(panelBadge("decisions", source({ loaded: false }))).toEqual(UNAVAILABLE);
  });

  // Each panel's loaded guard hides a stale panel that would otherwise draw a word. The stale value differs from the
  // unavailable word for its panel, so removing that panel's guard changes the badge and fails this case.
  const STALE: Record<PanelKey, Partial<Panels>> = {
    decisions: { governance: { allow_count: 0, deny_count: 1, unknown_count: 0 } },
    provenance: { provenance: { available: true } },
    publication: { publication: { state: "failed" } as PublicationPanel },
    recovery: { recovery: { state: "recovery-required" } as Panels["recovery"] },
    qualification: { qualification: { g1: false, h1: "unexecuted", containment: "not-established", claim_bearing: true } },
  };
  it.each([...PANEL_KEYS])("reads the %s badge unavailable before a load, even when its stale panel is present", (key) => {
    expect(panelBadge(key, withPanels(STALE[key], { loaded: false }))).toEqual(UNAVAILABLE);
  });

  // A count is absent or a stated number. The badge shows the first stated verdict above zero, in the order deny,
  // unknown, allow; an absent count the badge needs makes it unavailable. The digest changes no row.
  const rows: ReadonlyArray<readonly [number | undefined, number | undefined, number | undefined, PanelBadge]> = [
    [undefined, undefined, undefined, UNAVAILABLE],
    [0, 0, 0, { text: "no decisions", tone: "neutral" }],
    [2, 0, 0, { text: "2 allow verdicts only", tone: "neutral" }],
    [1, 0, 0, { text: "1 allow verdict only", tone: "neutral" }],
    [0, 2, 0, { text: "2 deny verdicts", tone: "danger" }],
    [0, 0, 2, { text: "2 unknown verdicts", tone: "warn" }],
    [undefined, 0, 0, UNAVAILABLE],
    [0, undefined, 0, UNAVAILABLE],
    [0, 0, undefined, UNAVAILABLE],
    [2, undefined, undefined, UNAVAILABLE],
    [undefined, 2, 2, { text: "2 deny verdicts", tone: "danger" }],
    [2, 1, undefined, { text: "1 deny verdict", tone: "danger" }],
  ];
  it.each(rows)("reads allow %s, deny %s, unknown %s as %j, with and without a policy digest", (allow, deny, unknown, badge) => {
    const bare = governance(allow, deny, unknown);
    const digested = governance(allow, deny, unknown, "sha256:ab");
    expect(panelBadge("decisions", withPanels({ governance: bare }))).toEqual(badge);
    expect(panelBadge("decisions", withPanels({ governance: digested }))).toEqual(badge);
  });
});

// A governance panel with the given counts. An undefined count is omitted, as the gateway omits a count it does not state.
function governance(allow?: number, deny?: number, unknown?: number, digest?: string): GovernancePanel {
  return {
    ...(allow === undefined ? {} : { allow_count: allow }),
    ...(deny === undefined ? {} : { deny_count: deny }),
    ...(unknown === undefined ? {} : { unknown_count: unknown }),
    ...(digest === undefined ? {} : { policy_digest: digest }),
  };
}

describe("the provenance badge", () => {
  it("reads available as a neutral word, and everything else as unavailable", () => {
    expect(panelBadge("provenance", withPanels({ provenance: { available: true } }))).toEqual({
      text: "available",
      tone: "neutral",
    });
    expect(panelBadge("provenance", withPanels({ provenance: { available: false } }))).toEqual(UNAVAILABLE);
    expect(panelBadge("provenance", withPanels({ provenance: null as unknown as Panels["provenance"] }))).toEqual(UNAVAILABLE);
  });
});

describe("the publication and recovery badges", () => {
  it("shows a failed publication as a danger word, and an unknown or lost one as a warning word", () => {
    const failed = withPanels({ publication: { state: "failed" } as PublicationPanel });
    expect(panelBadge("publication", failed)).toEqual({ text: "failed", tone: "danger" });
    const lost = withPanels({ publication: { state: "lost" } as unknown as PublicationPanel });
    expect(panelBadge("publication", lost)).toEqual({ text: "lost", tone: "warn" });
    const unknown = withPanels({ publication: { state: "unknown" } as PublicationPanel });
    expect(panelBadge("publication", unknown)).toEqual({ text: "unknown", tone: "warn" });
  });

  it("reads an unverified publication success as reported, neutral", () => {
    const published = withPanels({ publication: { state: "published" } as PublicationPanel });
    expect(panelBadge("publication", published)).toEqual({ text: "reported", tone: "neutral" });
  });

  it("keeps a verified success neutral: the word is shown, the success tone is not", () => {
    const published = withPanels({ publication: { state: "published" } as PublicationPanel }, VERIFIED);
    expect(panelBadge("publication", published)).toEqual({ text: "published", tone: "neutral" });
  });

  it("reads an absent publication panel as unavailable", () => {
    expect(panelBadge("publication", withPanels({ publication: null as unknown as PublicationPanel }))).toEqual(UNAVAILABLE);
  });

  it("shows a recovery that is required as a warning word", () => {
    const required = withPanels({ recovery: { state: "recovery-required" } as Panels["recovery"] });
    expect(panelBadge("recovery", required)).toEqual({ text: "recovery required", tone: "warn" });
  });
});

describe("the qualification badge and the warnings badge", () => {
  it("reads a boundary that is not established as not established", () => {
    expect(panelBadge("qualification", source())).toEqual({ text: "not established", tone: "neutral" });
    const contained = withPanels({
      qualification: { g1: false, h1: "unexecuted", containment: "not-established", claim_bearing: true },
    });
    expect(panelBadge("qualification", contained)).toEqual({ text: "not established", tone: "neutral" });
  });

  it("reads an absent qualification panel as unavailable", () => {
    expect(panelBadge("qualification", withPanels({ qualification: null as unknown as Panels["qualification"] }))).toEqual(
      UNAVAILABLE,
    );
  });

  it("reads a claim-bearing boundary whose containment is unavailable as not established, never as unavailable", () => {
    const claiming = withPanels({
      qualification: { g1: false, h1: "unexecuted", containment: "unavailable", claim_bearing: true },
    });
    expect(panelBadge("qualification", claiming)).toEqual({ text: "not established", tone: "neutral" });
  });

  it("reads a boundary that claims nothing as not established", () => {
    const silent = withPanels({
      qualification: { g1: false, h1: "unexecuted", containment: "unavailable", claim_bearing: false },
    });
    expect(panelBadge("qualification", silent)).toEqual({ text: "not established", tone: "neutral" });
  });

  it("reads a claim-bearing boundary with containment established as claim-bearing, in the warn tone", () => {
    // The contract admits no established containment today (CONTAINMENT_VALUES): this pins the badge for when it does.
    const established = withPanels({
      qualification: {
        g1: false,
        h1: "unexecuted",
        containment: "established" as unknown as Containment,
        claim_bearing: true,
      },
    });
    expect(panelBadge("qualification", established)).toEqual({ text: "claim-bearing", tone: "warn" });
  });

  it("counts the warnings in words, and shows nothing when there are none", () => {
    expect(warningBadge(source({ warnings: ["a"] }))).toEqual({ text: "1 warning", tone: "warn" });
    expect(warningBadge(source({ warnings: ["a", "b"] }))).toEqual({ text: "2 warnings", tone: "warn" });
    expect(warningBadge(source({ warnings: [] }))).toBeNull();
    expect(warningBadge(source({ loaded: false, warnings: ["a"] }))).toBeNull();
  });
});

describe("every tab key has a badge", () => {
  it("covers exactly the five governance panels", () => {
    const keys: PanelKey[] = [...PANEL_KEYS];
    expect(keys).toEqual(["decisions", "provenance", "publication", "recovery", "qualification"]);
    for (const key of keys) expect(panelBadge(key, source()).text).toBeTruthy();
  });
});
