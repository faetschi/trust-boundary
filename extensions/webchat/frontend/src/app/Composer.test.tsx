// @vitest-environment happy-dom
// The composer of the Chat view (Composer.tsx). It is a labelled, read-only region that submits nothing: not a form, no
// submit handler, no key handler, no network request. Its one behaviour is to say politely that it is not available,
// through the shell's announcer, and to draw the same sentence.
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import type { SessionHeader } from "../core/contract";
import { sessionDefault } from "../core/testing/builders";
import { click, mount, press, type Mounted } from "../test-support/dom";
import { AnnouncerContext } from "../ui/LiveRegion";
import { createAnnouncer, type Announcer } from "../ui/announcer";
import { Composer } from "./Composer";

let mounted: Mounted | null = null;
let announcer: Announcer | null = null;

function render(piVersion?: string): HTMLElement {
  const session: SessionHeader = piVersion === undefined ? sessionDefault() : { ...sessionDefault(), pi_version: piVersion };
  announcer = createAnnouncer();
  mounted = mount(
    <AnnouncerContext.Provider value={announcer}>
      <Composer session={session} />
    </AnnouncerContext.Provider>,
  );
  return mounted.container;
}

function q<T extends Element = HTMLElement>(selector: string): T {
  const element = mounted?.container.querySelector<T>(selector);
  if (!element) throw new Error(`no element for ${selector}`);
  return element;
}

function field(): HTMLTextAreaElement {
  return q<HTMLTextAreaElement>("textarea");
}

// The line the composer draws, and what the shell's announcer last said.
function drawn(): string {
  return q("#composer-status").textContent ?? "";
}

function spoken(): string {
  return announcer?.getSnapshot().text ?? "";
}

function chipTexts(): string[] {
  return Array.from(mounted?.container.querySelectorAll("[data-fact]") ?? []).map((fact) => fact.textContent?.trim() ?? "");
}

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  announcer = null;
  vi.unstubAllGlobals();
});

describe("the composer's semantics", () => {
  it("is a labelled section, and not a form", () => {
    render();
    const section = q("section[data-testid='composer']");
    expect(section.getAttribute("aria-label")).toBe("Message Pi: not available in this build");
    expect(mounted?.container.querySelector("form")).toBeNull();
  });

  it("has a read-only prompt field, marked disabled, and named by its reason", () => {
    render();
    expect(field().readOnly).toBe(true);
    expect(field().getAttribute("aria-disabled")).toBe("true");
    expect(field().getAttribute("aria-readonly")).toBe("true");
    expect(field().placeholder).toBe("Submitting turns is not available in this build.");
    expect(field().getAttribute("aria-describedby")).toBe("composer-reason");
    expect(q("#composer-reason").textContent).toBe(
      "Read-only view of a Pi session. This page cannot submit turns, cancel, or approve. Turn submission is a later milestone.",
    );
  });

  it("keeps the reason in the card, with its full text, for assistive technology, and never in a hidden element", () => {
    render();
    const reason = q("#composer-reason");
    expect(reason.closest("section[data-testid='composer']")).toBe(q("section[data-testid='composer']"));
    expect(reason.textContent).toBe(
      "Read-only view of a Pi session. This page cannot submit turns, cancel, or approve. Turn submission is a later milestone.",
    );
    expect(reason.hidden).toBe(false);
    expect(reason.closest("[hidden]")).toBeNull();
  });

  it("has one button, the grey send mark: aria-disabled, named for what it is, and nothing else is a control", () => {
    render();
    const buttons = Array.from(mounted?.container.querySelectorAll("button") ?? []);
    expect(buttons).toHaveLength(1);
    expect(buttons[0]?.getAttribute("aria-disabled")).toBe("true");
    expect(buttons[0]?.getAttribute("aria-label")).toBe("Send: not available in this build");
    expect(buttons[0]?.hasAttribute("disabled")).toBe(false);
    expect(mounted?.container.querySelector("[aria-haspopup], [role='menu'], [role='menuitem'], [role='listbox']")).toBeNull();
  });

  it("draws no '+' attach mark, and no text arrow (the send mark is an icon)", () => {
    render("0.87.1");
    const marks = Array.from(mounted?.container.querySelectorAll("[aria-hidden='true']") ?? []).map((element) => element.textContent);
    expect(marks).not.toContain("+");
    expect(marks).not.toContain("↑");
  });

  it("never names the closed tool vocabulary as this session's tools", () => {
    render("0.87.1");
    expect(mounted?.container.textContent).not.toContain("read, write, edit, bash");
  });

  it("adds no live region of its own: the shell's announcer is the one polite path", () => {
    render();
    expect(mounted?.container.querySelector("[role='status'], [role='alert'], [aria-live]")).toBeNull();
  });
});

describe("the composer does not submit", () => {
  // Every submit event in the document. The composer has no form, so none may fire from Send, Enter or typing.
  let submitted: Mock<(event: Event) => void>;
  beforeEach(() => {
    submitted = vi.fn<(event: Event) => void>();
    document.addEventListener("submit", submitted);
  });
  afterEach(() => {
    document.removeEventListener("submit", submitted);
  });

  it("makes no network request when the field is pressed, and says politely that it is not available", () => {
    const fetchSpy = vi.fn();
    const requestSpy = vi.fn();
    vi.stubGlobal("fetch", fetchSpy);
    vi.stubGlobal("XMLHttpRequest", requestSpy);
    render();
    expect(drawn()).toBe("");
    click(field());
    expect(fetchSpy).toHaveBeenCalledTimes(0);
    expect(requestSpy).toHaveBeenCalledTimes(0);
    expect(submitted).toHaveBeenCalledTimes(0);
    expect(field().value).toBe("");
    expect(drawn()).toBe("Not available: this page cannot submit turns.");
    expect(spoken()).toBe("Not available: this page cannot submit turns.");
  });

  it("says the same when the prompt field takes focus, and leaves focus where it is", () => {
    render();
    act(() => {
      field().focus();
    });
    expect(document.activeElement).toBe(field());
    expect(drawn()).toBe("Not available: this page cannot submit turns.");
    expect(spoken()).toBe("Not available: this page cannot submit turns.");
  });

  it("makes no request when Send is clicked or has Enter or Space pressed on it, and says it is not available", () => {
    const fetchSpy = vi.fn();
    vi.stubGlobal("fetch", fetchSpy);
    render();
    const send = q<HTMLButtonElement>("button");
    click(send);
    expect(drawn()).toBe("Not available: this page cannot submit turns.");
    press(send, "Enter");
    press(send, " ");
    expect(fetchSpy).toHaveBeenCalledTimes(0);
    expect(submitted).toHaveBeenCalledTimes(0);
    expect(field().value).toBe("");
  });

  it("puts the field, then Send, in the tab order, and nothing else in the composer", () => {
    render();
    const stops = Array.from(q("section[data-testid='composer']").querySelectorAll<HTMLElement>("a[href], button, input, select, textarea, [tabindex]"));
    expect(stops.map((stop) => stop.tagName.toLowerCase())).toEqual(["textarea", "button"]);
    expect(stops.every((stop) => stop.tabIndex >= 0)).toBe(true);
  });

  it("handles no typing and no Enter or Ctrl+Enter: the value stays empty and nothing is sent", () => {
    const fetchSpy = vi.fn();
    vi.stubGlobal("fetch", fetchSpy);
    render();
    press(field(), "a");
    press(field(), "Enter");
    press(field(), "Enter", { ctrlKey: true });
    press(field(), "Enter", { metaKey: true });
    expect(field().value).toBe("");
    expect(drawn()).toBe("");
    expect(spoken()).toBe("");
    expect(fetchSpy).toHaveBeenCalledTimes(0);
    expect(submitted).toHaveBeenCalledTimes(0);
  });
});

describe("the facts: non-interactive text with exact words", () => {
  it("reads 'Observed only · no approvals' and the Pi version, with tools unavailable", () => {
    render("0.87.1");
    expect(chipTexts()).toEqual(["Pi 0.87.1 · tools unavailable", "Observed only · no approvals"]);
  });

  it("reads an absent Pi version as unavailable, never as a version", () => {
    render();
    expect(chipTexts()).toEqual(["Pi version unavailable · tools unavailable", "Observed only · no approvals"]);
  });

  it("explains the first fact in its title, and draws both facts as text, never as buttons", () => {
    render("0.87.1");
    expect(q("span[title='This page observes the Pi session. It cannot approve, submit or cancel.']")).toBeTruthy();
    const facts = Array.from(mounted?.container.querySelectorAll("[data-fact]") ?? []);
    expect(facts).toHaveLength(2);
    expect(facts.every((fact) => fact.tagName === "SPAN" && fact.closest("button") === null)).toBe(true);
  });
});
