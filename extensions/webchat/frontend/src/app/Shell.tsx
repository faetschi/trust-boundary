import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Notice } from "../ui/Notice";
import { TabPanel, Tabs, panelId } from "../ui/Tabs";
import { ToggleGroup, type ToggleItem } from "../ui/ToggleGroup";
import { LiveRegion } from "../ui/LiveRegion";
import { useTheme } from "../theme/useTheme";
import { parseTheme } from "../theme/theme";
import type { ViewId } from "../core/store";
import { useLive } from "../live/context";
import { usePhone, useNarrow } from "./layout";
import { useWorkspace } from "./WorkspaceProvider";
import { ShellContext, type ShellControls } from "./shellContext";
import { Rail, RailToggle } from "./Rail";
import { bindingName } from "./sessionReadings";
import { INSPECTOR_ID, Inspector } from "./Inspector";
import { StatusPill } from "./StatusPill";
import { HELPER_ID, HelperBar } from "./HelperBar";
import { DetailsIcon } from "../ui/Icon";
import { governanceBadge } from "./inspector/panelBadge";
import { focusableIn, trapTarget } from "./focusTrap";
import { ChatView } from "./views/ChatView";
import { conversationHead } from "./views/chat/identity";
import { TrajectoryView } from "./views/TrajectoryView";
import { EVICTED_LABEL, evictedText, gapText, WINDOW_GAP_LABEL, WINDOW_GAP_TEXT } from "./status";
import { useUiPrefs, type PanelKey } from "./uiPrefs";
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

// The application shell: a skip link to the visible view, the header (the session breadcrumb, the status pill that opens the
// claim boundary, the Governance control with its badge, the overflow menu), the sessions rail, the workspace with its two views and its footer, and
// the governance inspector. The rail and the inspector collapse to icon columns at wide widths, and the inspector is a
// drawer below the drawer breakpoint. Their choices, and the selected governance panel, are persisted (uiPrefs.ts).
export function Shell({ version }: { readonly version: string }) {
  const model = useWorkspace();
  const { actions } = model;
  const live = useLive();
  const noSession = live !== null && live.model.activeId === null;
  const narrow = useNarrow();
  const phone = usePhone();
  const [prefs, updatePrefs] = useUiPrefs();
  const [drawerRequested, setDrawerRequested] = useState(false);
  // The details card is shown on request and for this session only. It is presentation, not a stored preference.
  const [helperOpen, setHelperOpen] = useState(false);
  // The phone rail is a disclosure. Its state is for this session only, so it is not persisted.
  const [phoneRailOpen, setPhoneRailOpen] = useState(false);
  // The record is shown in the inspector's panel body while a record is selected. A new selection sets it, and a governance
  // tab clears it.
  const [recordTab, setRecordTab] = useState(false);
  const drawerOpen = narrow && drawerRequested;
  const inspectorCollapsed = !narrow && prefs.inspectorCollapsed;
  const railExpanded = phone ? phoneRailOpen : !prefs.railCollapsed;
  const [theme, setTheme] = useTheme();
  const toggleRef = useRef<HTMLButtonElement | null>(null);
  const headingRef = useRef<HTMLHeadingElement | null>(null);
  // Where focus goes when the drawer closes: the row that opened it, else the toggle.
  const returnRef = useRef<HTMLElement | null>(null);
  // Set when the drawer is closed by a request that returns focus. The focus moves once the drawer has closed.
  const returnPending = useRef(false);
  // Set when the drawer is opened by a request that should move focus into it.
  const focusHeadingOnOpen = useRef(false);

  const openInspector = useCallback((returnTo?: HTMLElement | null) => {
    returnRef.current = returnTo ?? toggleRef.current;
    focusHeadingOnOpen.current = true;
    setDrawerRequested(true);
  }, []);

  // The page behind an open drawer is inert, and inert elements cannot take focus. So the return waits for that render to
  // commit: the effect below moves focus once the drawer is closed.
  const closeInspector = useCallback(() => {
    returnPending.current = true;
    setDrawerRequested(false);
  }, []);

  useEffect(() => {
    if (drawerOpen && focusHeadingOnOpen.current) {
      focusHeadingOnOpen.current = false;
      headingRef.current?.focus();
    }
    if (!drawerOpen && returnPending.current) {
      returnPending.current = false;
      const target = returnRef.current;
      returnRef.current = null;
      const focusTarget = target?.isConnected ? target : toggleRef.current;
      focusTarget?.focus();
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

  // Close record, and the record step of Escape: the record closes and an open drawer stays open, on its governance panel.
  // Focus moves to that panel's tab (the inspector's focus rule), so it stays inside the modal drawer.
  const dismissRecord = useCallback(() => {
    actions.clearRecord();
    setRecordTab(false);
  }, [actions]);

  // A new selection opens the record tab, and at narrow widths the drawer. At a wide width a collapsed column expands
  // to show it. Selecting the selected record again clears it, as the store does.
  const selectRecord = useCallback(
    (id: string, returnTo?: HTMLElement | null) => {
      const selecting = model.selectedId !== id;
      actions.selectRecord(id);
      if (!selecting) {
        setRecordTab(false);
        return;
      }
      setRecordTab(true);
      if (narrow) openInspector(returnTo);
      else if (prefs.inspectorCollapsed) updatePrefs({ inspectorCollapsed: false });
    },
    [actions, model.selectedId, narrow, openInspector, prefs.inspectorCollapsed, updatePrefs],
  );

  // Escape clears the selected record first, and leaves an open drawer open; the next Escape closes the drawer. It never
  // collapses the wide column. The details card is pinned by its own control: Escape does not close it.
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== "Escape") return;
      if (model.selectedId !== null) {
        dismissRecord();
        return;
      }
      if (drawerOpen) closeInspector();
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [model.selectedId, drawerOpen, dismissRecord, closeInspector]);

  // While the drawer is open it is a modal dialog: the page behind it is inert (see the markup), and Tab and Shift+Tab stay
  // inside it. The rule is in focusTrap.ts.
  useEffect(() => {
    if (!drawerOpen) return undefined;
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== "Tab") return;
      const drawer = document.getElementById(INSPECTOR_ID);
      if (!drawer) return;
      const next = trapTarget(focusableIn(drawer), document.activeElement, event.shiftKey);
      if (next === null) return;
      event.preventDefault();
      next.focus();
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [drawerOpen]);

  const toggleRail = useCallback(() => {
    if (phone) setPhoneRailOpen((open) => !open);
    else updatePrefs({ railCollapsed: !prefs.railCollapsed });
  }, [phone, prefs.railCollapsed, updatePrefs]);

  // The Governance control: the drawer at narrow widths, and the column's collapse at wide ones.
  const toggleInspector = useCallback(() => {
    if (narrow) {
      if (drawerOpen) closeInspector();
      else openInspector();
    } else {
      updatePrefs({ inspectorCollapsed: !prefs.inspectorCollapsed });
    }
  }, [narrow, drawerOpen, closeInspector, openInspector, prefs.inspectorCollapsed, updatePrefs]);

  const toggleInspectorColumn = useCallback(
    () => updatePrefs({ inspectorCollapsed: !prefs.inspectorCollapsed }),
    [prefs.inspectorCollapsed, updatePrefs],
  );
  // A governance tab, or an icon of the collapsed rail, shows its panel in place of a record: choosing one closes the record.
  const selectPanel = useCallback(
    (panel: PanelKey) => {
      if (model.selectedId !== null) actions.clearRecord();
      setRecordTab(false);
      updatePrefs({ panel });
    },
    [actions, model.selectedId, updatePrefs],
  );
  const expandTo = useCallback(
    (panel: PanelKey) => {
      if (model.selectedId !== null) actions.clearRecord();
      setRecordTab(false);
      updatePrefs({ panel, inspectorCollapsed: false });
    },
    [actions, model.selectedId, updatePrefs],
  );

  // A governance row of the details card opens that panel: the column expands at wide widths, and the drawer opens at narrow ones.
  const openPanel = useCallback(
    (panel: PanelKey) => {
      if (narrow) {
        selectPanel(panel);
        openInspector();
      } else {
        expandTo(panel);
      }
    },
    [narrow, selectPanel, openInspector, expandTo],
  );

  const controls = useMemo<ShellControls>(
    () => ({ narrow, drawerOpen, openInspector, closeInspector, selectRecord }),
    [narrow, drawerOpen, openInspector, closeInspector, selectRecord],
  );

  const governance = governanceBadge(model);
  const view = model.view;
  const skipLabel = view === "chat" ? "Skip to the chat transcript" : "Skip to the trajectory ledger";
  const hasNotice =
    model.gapNotice !== null || model.windowGap !== null || model.evicted > 0 || model.status.state === "error";

  return (
    <ShellContext.Provider value={controls}>
      <div className={styles.shell}>
        <a className={styles.skip} href={`#${panelId("view", view)}`} inert={drawerOpen}>
          {skipLabel}
        </a>
        {/* The open drawer is modal: a pointer press on the page behind it closes it. The scrim sits over the inert page, the
            header included, and under the drawer. It is hidden from assistive technology: keyboard users close the drawer with
            Escape or its own control. */}
        {drawerOpen ? <div data-testid="scrim" className={styles.scrim} aria-hidden="true" onClick={closeInspector} /> : null}
        {/* The header shares the body's column template: the brand sits over the rail, the title row and the claim strip
            over the workspace. It is the first header, and the body follows it (Shell.test.tsx). */}
        <header
          aria-label="Workspace header"
          className={styles.header}
          inert={drawerOpen}
          data-rail={railExpanded ? "expanded" : "collapsed"}
          data-inspector={inspectorCollapsed ? "collapsed" : "expanded"}
        >
          <div className={styles.brand}>
            <span className={styles.tile} aria-hidden="true">
              tb
            </span>
            <div className={styles.brandText}>
              <h1 className={styles.title}>tbound web chat</h1>
              <p className={styles.subtitle}>Pi chat workspace</p>
            </div>
            {/* At a desktop width the rail's collapse toggle is in the brand row; a phone's rail has its own strip. */}
            {phone ? null : <RailToggle expanded={railExpanded} onToggle={toggleRail} />}
          </div>
          <div className={styles.titleRow}>
            {/* The session title is the workspace heading: a breadcrumb (the source it is read from, then the session). */}
            <p className={styles.crumb}>{bindingName(model.source)} /</p>
            <h2 className={styles.sessionTitle}>{conversationHead(model.session, model.loaded).title}</h2>
            <div className={styles.controls}>
              <StatusPill model={model} noSession={noSession} />
              <button
                type="button"
                className={styles.drawerToggle}
                aria-controls={HELPER_ID}
                aria-expanded={helperOpen}
                onClick={() => setHelperOpen((open) => !open)}
              >
                <DetailsIcon />
                Details
              </button>
              <span className={styles.governanceControl}>
                <button
                  ref={toggleRef}
                  type="button"
                  className={styles.drawerToggle}
                  aria-controls={INSPECTOR_ID}
                  aria-expanded={narrow ? drawerOpen : !inspectorCollapsed}
                  aria-describedby="governance-badge"
                  onClick={toggleInspector}
                >
                  Governance
                </button>
                <span id="governance-badge" className={styles.governanceBadge} data-tone={governance.tone}>
                  {governance.text}
                </span>
              </span>
              {/* The theme switch is an overflow item: a native disclosure, so it is one Tab stop and opens with Enter or Space. */}
              <details className={styles.more}>
                <summary className={styles.moreSummary} aria-label="More options">
                  <span aria-hidden="true">…</span>
                </summary>
                <div className={styles.moreBody}>
                  <ToggleGroup
                    label="Theme"
                    items={THEME_ITEMS}
                    pressed={theme}
                    onPress={(value) => setTheme(parseTheme(value))}
                  />
                </div>
              </details>
            </div>
          </div>
        </header>
        <div
          className={styles.body}
          data-rail={railExpanded ? "expanded" : "collapsed"}
          data-inspector={inspectorCollapsed ? "collapsed" : "expanded"}
          data-details={helperOpen ? "open" : "closed"}
        >
          {/* The details card follows the header's controls in the DOM, so Tab reaches it right after the control that opens it. */}
          <HelperBar model={model} open={helperOpen} version={version} className={styles.helper} inert={drawerOpen} onOpenPanel={openPanel} />
          <Rail model={model} phone={phone} expanded={railExpanded} onToggle={toggleRail} inert={drawerOpen} />
          <main aria-label="Workspace" className={styles.main} inert={drawerOpen}>
            {hasNotice ? (
              <div className={styles.notices}>
                {model.gapNotice ? (
                  <Notice label="Continuity gap" tone="warning">
                    {gapText(model.gapNotice)}
                  </Notice>
                ) : null}
                {model.windowGap ? (
                  <Notice label={WINDOW_GAP_LABEL} tone="warning">
                    {WINDOW_GAP_TEXT}
                  </Notice>
                ) : null}
                {model.evicted > 0 ? (
                  <Notice label={EVICTED_LABEL} tone="info">
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
            <div className={styles.views} data-view={view}>
              <TabPanel idPrefix="view" id="chat" value={view}>
                <ChatView model={model} />
              </TabPanel>
              <TabPanel idPrefix="view" id="trajectory" value={view}>
                <TrajectoryView model={model} />
              </TabPanel>
            </div>
          </main>
          <Inspector
            model={model}
            narrow={narrow}
            open={drawerOpen}
            headingRef={headingRef}
            collapsed={inspectorCollapsed}
            panel={prefs.panel}
            recordTab={recordTab}
            onClose={closeInspector}
            onToggleCollapsed={toggleInspectorColumn}
            onExpandTo={expandTo}
            onSelectPanel={selectPanel}
            onCloseRecord={dismissRecord}
          />
        </div>
        <LiveRegion />
      </div>
    </ShellContext.Provider>
  );
}
