import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Notice } from "../ui/Notice";
import { TabPanel, Tabs, panelId } from "../ui/Tabs";
import { ToggleGroup, type ToggleItem } from "../ui/ToggleGroup";
import { LiveRegion } from "../ui/LiveRegion";
import { useTheme } from "../theme/useTheme";
import { parseTheme } from "../theme/theme";
import { isVerifiedProjection } from "../core/format";
import type { ViewId } from "../core/store";
import { useNarrow } from "./layout";
import { useWorkspace } from "./WorkspaceProvider";
import { ShellContext, type ShellControls } from "./shellContext";
import { Rail } from "./Rail";
import { Footer } from "./Footer";
import { INSPECTOR_ID, Inspector } from "./Inspector";
import { ChatView } from "./views/ChatView";
import { TrajectoryView } from "./views/TrajectoryView";
import { evictedText, gapText, WINDOW_GAP_TEXT, type ConnectionStatus } from "./status";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Shell.module.css";

const VIEW_ITEMS: ReadonlyArray<{ readonly id: ViewId; readonly label: string }> = [
  { id: "chat", label: "Chat" },
  { id: "trajectory", label: "Trajectory" },
];

const THEME_ITEMS: readonly ToggleItem[] = [
  { value: "system", label: "System" },
  { value: "light", label: "Light" },
  { value: "dark", label: "Dark" },
];

function sourceText(model: WorkspaceModel): string {
  const scope = isVerifiedProjection(model) ? "" : " · unverified";
  return `source: ${model.source}${scope}`;
}

function connectionText(status: ConnectionStatus): string {
  return status.warning ? `${status.label} · ${status.warning}` : status.label;
}

// The application shell: a skip link to the visible view, the header (title, source and
// connection badges, the claim boundary, the theme and drawer controls), the sessions rail, the
// main workspace with its two views, the inspector, and the footer metrics.
export function Shell({ version }: { readonly version: string }) {
  const model = useWorkspace();
  const { actions } = model;
  const narrow = useNarrow();
  const [drawerRequested, setDrawerRequested] = useState(false);
  const drawerOpen = narrow && drawerRequested;
  const [theme, setTheme] = useTheme();
  const toggleRef = useRef<HTMLButtonElement | null>(null);
  const headingRef = useRef<HTMLHeadingElement | null>(null);
  // Where focus goes when the drawer closes: the row that opened it, else the toggle.
  const returnRef = useRef<HTMLElement | null>(null);
  // Set when the drawer is opened by a request that should move focus into it.
  const focusHeadingOnOpen = useRef(false);

  const openInspector = useCallback((returnTo?: HTMLElement | null) => {
    returnRef.current = returnTo ?? toggleRef.current;
    focusHeadingOnOpen.current = true;
    setDrawerRequested(true);
  }, []);

  const closeInspector = useCallback(() => {
    const target = returnRef.current;
    returnRef.current = null;
    setDrawerRequested(false);
    const focusTarget = target?.isConnected ? target : toggleRef.current;
    focusTarget?.focus();
  }, []);

  useEffect(() => {
    if (drawerOpen && focusHeadingOnOpen.current) {
      focusHeadingOnOpen.current = false;
      headingRef.current?.focus();
    }
  }, [drawerOpen]);

  // The drawer belongs to the narrow layout only. Leaving that layout forgets the request, so a later
  // narrow layout starts closed and never reopens by itself. Entering it with the drawer closed moves
  // focus out of the inspector, because the inspector is inert while closed.
  useEffect(() => {
    if (!narrow) {
      setDrawerRequested(false);
      returnRef.current = null;
      focusHeadingOnOpen.current = false;
      return;
    }
    const active = document.activeElement;
    if (!drawerRequested && active instanceof HTMLElement && active.closest(`#${INSPECTOR_ID}`)) {
      toggleRef.current?.focus();
    }
  }, [narrow]);

  const clearRecord = useCallback(() => {
    actions.clearRecord();
    if (drawerOpen) closeInspector();
  }, [actions, drawerOpen, closeInspector]);

  const selectRecord = useCallback(
    (id: string, returnTo?: HTMLElement | null) => {
      const selecting = model.selectedId !== id;
      actions.selectRecord(id);
      if (narrow && selecting) openInspector(returnTo);
    },
    [actions, model.selectedId, narrow, openInspector],
  );

  // Escape clears the selected record first; otherwise it closes an open drawer.
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== "Escape") return;
      if (model.selectedId !== null) {
        clearRecord();
        return;
      }
      if (drawerOpen) closeInspector();
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [model.selectedId, drawerOpen, clearRecord, closeInspector]);

  const controls = useMemo<ShellControls>(
    () => ({ narrow, drawerOpen, openInspector, closeInspector, selectRecord, clearRecord }),
    [narrow, drawerOpen, openInspector, closeInspector, selectRecord, clearRecord],
  );

  const view = model.view;
  const skipLabel = view === "chat" ? "Skip to the chat transcript" : "Skip to the trajectory ledger";
  const hasNotice =
    model.gapNotice !== null || model.windowGap !== null || model.evicted > 0 || model.status.state === "error";

  return (
    <ShellContext.Provider value={controls}>
      <div className={styles.shell}>
        <a className={styles.skip} href={`#${panelId("view", view)}`}>
          {skipLabel}
        </a>
        <header aria-label="Workspace header" className={styles.header}>
          <div className={styles.brand}>
            <h1 className={styles.title}>tbound web chat</h1>
            <p className={styles.subtitle}>Pi chat workspace</p>
          </div>
          <div className={styles.badges}>
            <span className={styles.badge} data-source={model.source}>
              {sourceText(model)}
            </span>
            <span className={styles.badge} data-connection={model.status.state}>
              {connectionText(model.status)}
            </span>
          </div>
          <div className={styles.controls}>
            <button
              ref={toggleRef}
              type="button"
              className={styles.drawerToggle}
              aria-controls={INSPECTOR_ID}
              aria-expanded={narrow ? drawerOpen : undefined}
              onClick={() => (drawerOpen ? closeInspector() : openInspector())}
            >
              Governance
            </button>
            <ToggleGroup
              label="Theme"
              items={THEME_ITEMS}
              pressed={theme}
              onPress={(value) => setTheme(parseTheme(value))}
            />
          </div>
          <div className={styles.claimRow}>
            <Notice label="Claim boundary" tone="claim">
              <p className={styles.claimText}>
                Presentation only: the supervisor retains policy, effect and publication authority.
              </p>
              <dl className={styles.claim}>
                <div>
                  <dt>G1</dt>
                  <dd>false</dd>
                </div>
                <div>
                  <dt>H1</dt>
                  <dd>unexecuted</dd>
                </div>
                <div>
                  <dt>containment</dt>
                  <dd>not-established</dd>
                </div>
                <div>
                  <dt>claim-bearing</dt>
                  <dd>false</dd>
                </div>
              </dl>
            </Notice>
          </div>
        </header>
        <div className={styles.body}>
          <Rail model={model} />
          <main aria-label="Workspace" className={styles.main}>
            {hasNotice ? (
              <div className={styles.notices}>
                {model.gapNotice ? (
                  <Notice label="Continuity gap" tone="warning">
                    {gapText(model.gapNotice)}
                  </Notice>
                ) : null}
                {model.windowGap ? (
                  <Notice label="Retained window" tone="warning">
                    {WINDOW_GAP_TEXT}
                  </Notice>
                ) : null}
                {model.evicted > 0 ? (
                  <Notice label="Retained window" tone="info">
                    {evictedText(model.evicted)}
                  </Notice>
                ) : null}
                {model.status.state === "error" ? (
                  <Notice label="Stream stopped" tone="warning">
                    {model.status.detail || "The stream stopped."}
                  </Notice>
                ) : null}
              </div>
            ) : null}
            <Tabs
              label="Workspace view"
              idPrefix="view"
              items={VIEW_ITEMS}
              value={view}
              onValueChange={(id) => {
                const next = VIEW_ITEMS.find((item) => item.id === id);
                if (next) actions.setView(next.id);
              }}
            />
            <TabPanel idPrefix="view" id="chat" value={view}>
              <ChatView model={model} />
            </TabPanel>
            <TabPanel idPrefix="view" id="trajectory" value={view}>
              <TrajectoryView model={model} />
            </TabPanel>
          </main>
          <Inspector
            model={model}
            narrow={narrow}
            open={drawerOpen}
            headingRef={headingRef}
            onClose={model.selectedId !== null ? clearRecord : closeInspector}
          />
        </div>
        <Footer model={model} version={version} />
        <LiveRegion />
      </div>
    </ShellContext.Provider>
  );
}
