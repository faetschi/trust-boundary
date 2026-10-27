// @vitest-environment happy-dom
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { Shell } from "../app/Shell";
import { WorkspaceProvider } from "../app/WorkspaceProvider";
import { snapshotOf } from "../core/testing/builders";
import { click, installMatchMedia, mount, press, settle, type MatchMediaShim, type Mounted } from "../test-support/dom";
import { fakeWorkspace } from "../test-support/fakes";
import { createFakeGateway, NOW_MS, SESSION_ID, TURN_1, type FakeEvent, type FakeGateway } from "../test-support/liveGateway";
import { createLiveClient } from "./client";
import { ACTIVE_SESSION_KEY, createLiveController, cursorKey, type LiveController, type LiveStorage } from "./controller";
import { LiveRoot } from "./LiveRoot";
import { COMPOSER_INPUT_ID } from "./LiveComposer";

let mounted: Mounted | null = null;
let shim: MatchMediaShim | null = null;
let controller: LiveController | null = null;

function memoryStorage(initial: Record<string, string> = {}): LiveStorage {
  const data = new Map(Object.entries(initial));
  return { get: (key) => data.get(key) ?? null, set: (key, value) => void data.set(key, value) };
}

let keyCounter = 0;
function newController(gateway: FakeGateway, storage: LiveStorage = memoryStorage()): LiveController {
  return createLiveController({
    client: createLiveClient({
      fetch: gateway.fetch,
      nowMs: () => NOW_MS,
      randomBytes: (bytes) => {
        keyCounter += 1;
        bytes.fill(9);
        bytes[0] = keyCounter & 0xff;
        bytes[1] = (keyCounter >> 8) & 0xff;
      },
    }),
    base: fakeWorkspace(snapshotOf()).deps,
    storage,
    sleep: async () => {},
  });
}

const TRANSCRIPT: Omit<FakeEvent, "seq">[] = [
  { kind: "turn_admitted", turn_id: TURN_1, text: "fix the failing test" },
  { kind: "turn_linked", turn_id: TURN_1 },
  { kind: "assistant_message", turn_id: TURN_1, text: "I will **read** the file. <script>alert(1)</script>" },
  { kind: "tool_call", turn_id: TURN_1, tool: "bash", tool_call_id: "call_1", verdict: "DENY", reason_code: "policy_rule_deny", hint: "use the read tool" },
  { kind: "tool_result", turn_id: TURN_1, tool: "bash", tool_call_id: "call_1", verdict: "DENY", text: "denied" },
  { kind: "turn_terminal", turn_id: TURN_1, outcome: "no_diff" },
];

const DONE = { id: TURN_1, ordinal: 1, state: "terminal", prompt: "fix the failing test", outcome: "no_diff" } as const;
const RUNNING = { id: TURN_1, ordinal: 1, state: "linked", prompt: "fix the failing test" } as const;

async function render(gateway: FakeGateway, options: { storage?: LiveStorage; open?: boolean } = {}): Promise<void> {
  shim = installMatchMedia(1440);
  controller = newController(gateway, options.storage);
  const live = controller;
  mounted = mount(
    <LiveRoot controller={live}>
      <Shell version="0.1.0" />
    </LiveRoot>,
  );
  await settle();
  await settle();
  if (options.open !== false) {
    click(q(`button[aria-label], nav button`) && sessionButton());
    await settle();
    await settle();
    await settle();
  }
}

function q<T extends Element = HTMLElement>(selector: string): T {
  const element = mounted?.container.querySelector<T>(selector);
  if (!element) throw new Error(`no element for ${selector}`);
  return element;
}

function all<T extends Element = HTMLElement>(selector: string): T[] {
  return Array.from(mounted?.container.querySelectorAll<T>(selector) ?? []);
}

function sessionButton(): HTMLElement {
  return q("ul[aria-label='Sessions'] button");
}

function button(name: string): HTMLButtonElement {
  const match = all<HTMLButtonElement>("button").find((el) => el.textContent?.trim() === name);
  if (!match) throw new Error(`no button "${name}"`);
  return match;
}

function type(field: HTMLInputElement | HTMLTextAreaElement, text: string): void {
  const proto = field instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
  act(() => {
    setter?.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

function composer(): HTMLTextAreaElement {
  return q<HTMLTextAreaElement>(`#${COMPOSER_INPUT_ID}`);
}

beforeEach(() => {
  document.body.innerHTML = "";
});

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  shim?.restore();
  shim = null;
  controller = null;
});

describe("live UI: opening a session", () => {
  it("lists the sessions with project, provider and mode, and opens one to show its transcript", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT, turns: [DONE] });
    await render(gateway, { open: false });
    const row = sessionButton();
    expect(row.textContent).toContain("demo · prof");
    expect(row.textContent).toContain("mode review");
    click(row);
    await settle();
    await settle();
    await settle();
    expect(row.getAttribute("aria-current")).toBe("true");
    expect(q("[data-source]").textContent).toBe("source: live");
    const log = q("[role='log']");
    expect(log.textContent).toContain("fix the failing test");
    expect(log.textContent).toContain("policy_rule_deny");
    expect(log.textContent).toContain("use the read tool");
  });

  it("keeps focus on the session row it was chosen with", async () => {
    await render(createFakeGateway({ events: TRANSCRIPT, turns: [DONE] }), { open: false });
    const row = sessionButton();
    row.focus();
    click(row);
    await settle();
    await settle();
    expect(document.activeElement).toBe(row);
  });

  it("renders model text through the sanitizing markdown path and keeps governance state out of the transcript", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT, turns: [DONE] });
    await render(gateway);
    const log = q("[role='log']");
    expect(log.querySelector("strong")?.textContent).toBe("read");
    expect(mounted?.container.querySelector("script")).toBeNull();
    expect(log.textContent).toContain("<script>alert(1)</script>");
    const panel = q("[role='group'][aria-label='Session state']");
    expect(log.contains(panel)).toBe(false);
    expect(panel.textContent).toContain("revision 4");
  });

  it("is not labelled read-only, and adds no second live region", async () => {
    await render(createFakeGateway({ events: TRANSCRIPT, turns: [DONE] }));
    expect(mounted?.container.textContent).not.toContain("not available in this build");
    expect(mounted?.container.textContent).not.toContain("Read-only projection");
    expect(all("[aria-live='polite'],[role='status'],[role='alert']")).toHaveLength(1);
  });

  it("shows the remaining budget as the budget less what is consumed and reserved", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT, turns: [DONE] });
    gateway.state.consumed = { host_calls: 3, tokens: 100, duration_seconds: 10 };
    gateway.state.reserved = { host_calls: 2, tokens: 50, duration_seconds: 5 };
    await render(gateway);
    const group = q("[role='group'][aria-label='Remaining budget']");
    expect(group.textContent).toContain("host calls 5 left of 10");
    expect(group.textContent).toContain("tokens 850 left of 1000");
    expect(group.textContent).toContain("seconds 45 left of 60");
  });

  it("says when the budget is used up", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT, turns: [DONE] });
    gateway.state.consumed = { host_calls: 10, tokens: 0, duration_seconds: 0 };
    await render(gateway);
    expect(q("[role='group'][aria-label='Remaining budget']").textContent).toContain("budget used up");
  });

  it("restores the open session after a reload and says how many events arrived meanwhile", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT, turns: [DONE] });
    const storage = memoryStorage({ [ACTIVE_SESSION_KEY]: SESSION_ID, [cursorKey(SESSION_ID)]: "4" });
    await render(gateway, { storage, open: false });
    await settle();
    await settle();
    expect(sessionButton().getAttribute("aria-current")).toBe("true");
    expect(q("[role='note'][aria-label='Events while away']").textContent).toContain("2 events arrived while this page was closed");
  });

  it("tells a blocked session apart, and gives the reason sending is closed", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT, turns: [{ ...DONE, state: "unknown", outcome: undefined } as never] });
    gateway.state.blocked = true;
    await render(gateway);
    const note = q("[role='note'][aria-label='Session blocked']");
    expect(note.textContent).toContain("blocked");
    expect(note.textContent).toContain("turn-1");
    expect(q("#live-composer-reason").textContent).toContain("blocked");
  });
});

describe("live UI: the composer", () => {
  it("sends the prompt on Enter under the displayed revision, and clears the draft once it was accepted", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT, turns: [DONE] });
    await render(gateway);
    type(composer(), "run the tests");
    press(composer(), "Enter");
    await settle();
    await settle();
    const [sent] = gateway.callsTo("POST", "/turns");
    expect(sent?.body).toMatchObject({ prompt: "run the tests", expected_revision: 4 });
    expect(sent?.headers["X-TBound-CSRF"]).toBe(gateway.state.csrf);
    expect(composer().value).toBe("");
  });

  it("keeps a newline for Shift+Enter, and does not send an empty draft", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT, turns: [DONE] });
    await render(gateway);
    press(composer(), "Enter");
    await settle();
    press(composer(), "Enter", { shiftKey: true });
    await settle();
    expect(gateway.callsTo("POST", "/turns")).toHaveLength(0);
    expect(button("Send").getAttribute("aria-disabled")).toBe("true");
  });

  it("keeps Send and Cancel in the tab order when they cannot act, with the reason beside them", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT.slice(0, 3), turns: [RUNNING] });
    await render(gateway);
    type(composer(), "next prompt");
    const send = button("Send");
    expect(send.disabled).toBe(false);
    expect(send.getAttribute("aria-disabled")).toBe("true");
    expect(q("#live-composer-reason").textContent).toBe("A turn is in progress.");
    expect(composer().getAttribute("aria-describedby")).toBe("live-composer-reason");
    click(send);
    await settle();
    expect(gateway.callsTo("POST", "/turns")).toHaveLength(0);
    expect(composer().value).toBe("next prompt");
    expect(button("Cancel turn").getAttribute("aria-disabled")).toBe("false");
  });

  it("cancels the running turn", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT.slice(0, 3), turns: [RUNNING] });
    await render(gateway);
    click(button("Cancel turn"));
    await settle();
    await settle();
    const [cancel] = gateway.callsTo("POST", "/cancel");
    expect(cancel?.url).toBe(`/api/v1/sessions/${SESSION_ID}/turns/turn-1/cancel`);
    expect(cancel?.body).toMatchObject({ expected_revision: 4 });
    expect(button("Cancel turn").getAttribute("aria-disabled")).toBe("true");
  });

  it("shows an unknown outcome, keeps the draft, and retries under the same request key", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT, turns: [DONE] });
    await render(gateway);
    gateway.inject((call) => call.method === "POST" && call.url.endsWith("/turns"), { reject: "connection reset" }, 3);
    type(composer(), "run the tests");
    press(composer(), "Enter");
    await settle();
    await settle();
    await settle();
    const notice = q("[data-testid='action-notice']");
    expect(notice.textContent).toContain("Outcome unknown");
    expect(notice.textContent).toContain("same request key");
    expect(composer().value).toBe("run the tests");
    expect(q("#live-composer-reason").textContent).toContain("unknown outcome");

    click(button("Retry with the same request key"));
    await settle();
    await settle();
    const sent = gateway.callsTo("POST", "/turns");
    expect(sent).toHaveLength(4);
    expect(sent[3]?.body).toEqual(sent[0]?.body);
    expect(all("[data-testid='action-notice']")).toHaveLength(0);
    expect(composer().value).toBe("");
  });

  it("offers Dismiss alone for a refusal that would only be refused again", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT, turns: [DONE] });
    await render(gateway);
    gateway.inject((call) => call.method === "POST", { status: 409, body: { error: { code: "PROJECT_BUSY", detail: "another session" } } });
    type(composer(), "run the tests");
    press(composer(), "Enter");
    await settle();
    await settle();
    expect(q("[data-testid='action-notice']").textContent).toContain("PROJECT_BUSY");
    expect(all("button").some((el) => el.textContent === "Retry with the same request key")).toBe(false);
    click(button("Dismiss"));
    await settle();
    expect(all("[data-testid='action-notice']")).toHaveLength(0);
  });
});

describe("live UI: reviewing a candidate", () => {
  async function review(diff = "-a\n+b\n"): Promise<FakeGateway> {
    const gateway = createFakeGateway({ events: TRANSCRIPT.slice(0, 3), turns: [RUNNING] });
    await render(gateway);
    gateway.awaitReview("sha256:cand1", [{ path: "a.go", op: "modify", diff }]);
    await settle();
    await settle();
    await settle();
    return gateway;
  }

  it("shows the candidate's digest and diff, outside the transcript", async () => {
    await review();
    const card = q("section[aria-label='Review of turn-1']");
    expect(card.getAttribute("data-digest")).toBe("sha256:cand1");
    expect(card.textContent).toContain("candidate sha256:cand1");
    expect(card.textContent).toContain("a.go");
    const diff = q("pre[aria-label='Diff of a.go']");
    expect(diff.textContent).toBe("-a\n+b\n");
    expect(diff.querySelectorAll("span")).toHaveLength(2);
    // At the foot of the transcript, after the turns and not inside one of them.
    expect(q("[role='log']").contains(card)).toBe(true);
    expect(card.closest("section[aria-label='turn-1']")).toBeNull();
    expect(q("#live-composer-reason").textContent).toContain("waiting for your review");
    click(button("Go to the review"));
    expect(document.activeElement?.id).toBe("live-review-heading");
  });

  it("draws a hostile diff as text and builds no element from it", async () => {
    await review("-a\n+<img src=x onerror=alert(1)><script>alert(2)</script>\n");
    const diff = q("pre[aria-label='Diff of a.go']");
    expect(diff.textContent).toContain("<img src=x onerror=alert(1)>");
    expect(mounted?.container.querySelector("img")).toBeNull();
    expect(mounted?.container.querySelector("script")).toBeNull();
  });

  it("publishes the displayed candidate, and returns focus to the composer when the card goes", async () => {
    const gateway = await review();
    const displayed = q("section[aria-label='Review of turn-1']").getAttribute("data-digest");
    button("Publish").focus();
    click(button("Publish"));
    await settle();
    await settle();
    await settle();
    const [resolve] = gateway.callsTo("POST", "/resolve");
    expect(resolve?.url).toBe(`/api/v1/sessions/${SESSION_ID}/turns/turn-1/resolve`);
    expect(resolve?.body).toMatchObject({ candidate_digest: displayed, decision: "publish", expected_revision: gateway.state.revision - 1 });
    expect(all("section[aria-label='Review of turn-1']")).toHaveLength(0);
    expect(document.activeElement).toBe(composer());
  });

  it("discards with the same binding", async () => {
    const gateway = await review();
    click(button("Discard"));
    await settle();
    await settle();
    expect(gateway.callsTo("POST", "/resolve")[0]?.body).toMatchObject({ candidate_digest: "sha256:cand1", decision: "discard" });
  });

  it.each([false, true])("offers same-key recovery for a restored decision without a candidate card, terminal=%s", async (terminal) => {
    const gateway = createFakeGateway({ turns: [RUNNING] });
    const storage = memoryStorage();
    gateway.awaitReview("sha256:cand1");
    const first = newController(gateway, storage);
    await first.start();
    first.select(SESSION_ID);
    await first.refreshView();
    await settle();
    const shown = first.getSnapshot().candidate;
    if (shown.status !== "ready") throw new Error("no candidate shown");
    gateway.inject((call) => call.method === "POST" && call.url.endsWith("/resolve"), { lose: "lost answer" }, 3);
    expect(await first.resolve(shown.candidate, "publish")).toBe(false);
    const original = gateway.callsTo("POST", "/resolve")[0]?.body;
    if (terminal) {
      const turn = gateway.state.turns[0];
      if (turn === undefined) throw new Error("no turn");
      turn.state = "terminal";
      turn.outcome = "published";
      gateway.state.revision += 1;
    }
    await render(gateway, { storage, open: false });
    expect(controller?.getSnapshot().action.status).toBe("unknown");
    expect(all("section[aria-label='Review of turn-1']")).toHaveLength(0);
    const notice = q("[role='note'][aria-label='Review decision request']");
    expect(notice.textContent).toContain("Outcome unknown");
    expect(notice.textContent).toContain("same request key");
    expect(q("#live-composer-reason").textContent).toContain("unknown outcome");
    click(button("Retry with the same request key"));
    await settle();
    await settle();
    expect(gateway.callsTo("POST", "/resolve")).toHaveLength(4);
    expect(gateway.callsTo("POST", "/resolve").at(-1)?.body).toEqual(original);
    expect(gateway.state.turns).toHaveLength(1);
    expect(controller?.getSnapshot().action.done).toEqual({ kind: "resolve", replayed: true });
    expect(q("[role='note'][aria-label='Review decision request']").textContent).toContain("already recorded");
  });

  it("refuses a stale candidate with the service's code, and shows the new one", async () => {
    const gateway = await review();
    gateway.state.candidate = { digest: "sha256:cand2", changes: [{ path: "b.go", op: "add", diff: "+x\n" }] };
    click(button("Publish"));
    await settle();
    await settle();
    expect(gateway.callsTo("POST", "/resolve")[0]?.body).toMatchObject({ candidate_digest: "sha256:cand1" });
    expect(q("[data-testid='action-notice']").textContent).toContain("CANDIDATE_MISMATCH");
    const turn = gateway.state.turns[0];
    if (turn === undefined) throw new Error("no turn");
    turn.candidate_digest = "sha256:cand2";
    gateway.state.revision += 1;
    await act(async () => {
      await controller?.refreshView();
    });
    await settle();
    await settle();
    expect(q("section[aria-label='Review of turn-1']").getAttribute("data-digest")).toBe("sha256:cand2");
    expect(q("pre[aria-label='Diff of b.go']").textContent).toBe("+x\n");
  });

  it("says when the candidate could not be read and offers to read it again", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT.slice(0, 3), turns: [RUNNING] });
    await render(gateway);
    gateway.inject((call) => call.url.endsWith("/candidate"), { status: 409, body: { error: { code: "REVIEW_NOT_PENDING", detail: "closed" } } });
    gateway.awaitReview("sha256:cand1");
    await settle();
    await settle();
    await settle();
    expect(q("[role='note'][aria-label='Candidate not loaded']").textContent).toContain("REVIEW_NOT_PENDING");
    click(button("Read the candidate again"));
    await settle();
    await settle();
    expect(all("section[aria-label='Review of turn-1']")).toHaveLength(1);
  });
});

describe("live UI: creating a session", () => {
  it("sends a create request under a fresh key and opens the new session", async () => {
    const gateway = createFakeGateway({ events: TRANSCRIPT, turns: [DONE] });
    await render(gateway, { open: false });
    const form = q<HTMLFormElement>("form[aria-label='New session']");
    const [project, provider] = Array.from(form.querySelectorAll<HTMLInputElement>("input[list]"));
    type(project as HTMLInputElement, "demo");
    type(provider as HTMLInputElement, "prof");
    click(button("Create session"));
    await settle();
    await settle();
    await settle();
    const [create] = gateway.callsTo("POST", "/api/v1/sessions");
    expect(create?.body).toMatchObject({
      project_id: "demo",
      provider_profile: "prof",
      publication_mode: "review",
      budget: { host_calls: 50, tokens: 100000, duration_seconds: 600 },
    });
    expect((create?.body as { request_key: string }).request_key).toMatch(/^ck-/);
    expect(controller?.getSnapshot().activeId).toBe("psess-new");
  });

  it("names the problem instead of sending an incomplete form", async () => {
    const gateway = createFakeGateway();
    await render(gateway, { open: false });
    click(button("Create session"));
    await settle();
    expect(q("[role='note'][aria-label='New session']").textContent).toContain("Project is required");
    expect(gateway.callsTo("POST", "/api/v1/sessions")).toHaveLength(0);
  });
});

describe("live UI: which gateway answered", () => {
  it("shows the pairing instructions when the gateway wants a pairing", async () => {
    shim = installMatchMedia(1440);
    mounted = mount(<App version="0.1.0" detect={async () => "unpaired"} />);
    await settle();
    expect(q("main[aria-label='Pairing required']").textContent).toContain("not paired");
    expect(mounted.container.querySelector("textarea")).toBeNull();
  });

  it("builds the live shell for a live gateway", async () => {
    shim = installMatchMedia(1440);
    const gateway = createFakeGateway({ events: TRANSCRIPT, turns: [DONE] });
    controller = newController(gateway);
    mounted = mount(<App version="0.1.0" detect={async () => "live"} controller={controller} />);
    await settle();
    await settle();
    expect(sessionButton().textContent).toContain("demo · prof");
  });

  it("labels a fixture projection as a demonstration, and a live one as none", async () => {
    shim = installMatchMedia(1440);
    mounted = mount(
      <WorkspaceProvider deps={fakeWorkspace(snapshotOf({ source: "fixture" })).deps}>
        <Shell version="0.1.0" />
      </WorkspaceProvider>,
    );
    await settle();
    expect(q("[data-demonstration]").textContent).toBe("demonstration · not a live session");
    mounted.unmount();
    mounted = null;
    await render(createFakeGateway({ events: TRANSCRIPT, turns: [DONE] }));
    expect(document.querySelector("[data-demonstration]")).toBeNull();
  });

  it("keeps the read-only demonstration path for any other gateway", async () => {
    shim = installMatchMedia(1440);
    const original = window.fetch;
    window.fetch = () => Promise.reject(new TypeError("no gateway"));
    try {
      mounted = mount(<App version="0.1.0" detect={async () => "demo"} />);
      await settle();
      await settle();
      expect(q("textarea[aria-label='Message Pi']").getAttribute("aria-readonly")).toBe("true");
      expect(mounted.container.querySelector("ul[aria-label='Sessions'] button")).toBeNull();
    } finally {
      window.fetch = original;
    }
  });
});
