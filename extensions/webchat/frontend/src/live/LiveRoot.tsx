import { useEffect, useMemo, useState, useSyncExternalStore, type ReactNode } from "react";
import { browserDeps, browserLiveDeps } from "../app/browser";
import { WorkspaceProvider } from "../app/WorkspaceProvider";
import { createWorkspaceStore } from "../app/workspaceStore";
import { createLiveClient } from "./client";
import { LiveContext } from "./context";
import { createLiveController, type LiveController } from "./controller";

function browserController(): LiveController {
  const live = browserLiveDeps();
  const base = browserDeps();
  return createLiveController({
    client: createLiveClient({ fetch: live.fetch, nowMs: base.nowMs, randomBytes: live.randomBytes }),
    base,
    storage: live.storage,
    sleep: live.sleep,
  });
}

interface LiveRootProps {
  // Injected in tests. The app builds the browser one.
  readonly controller?: LiveController;
  readonly children: ReactNode;
}

// Owns the live controller for the life of the page, and one workspace store per open session. Choosing another session
// swaps the store under the same shell: the old event stream closes and a new one opens, snapshot first, and focus stays
// where it was. With no session open the store stays unstarted.
export function LiveRoot({ controller: injected, children }: LiveRootProps) {
  const [controller] = useState(() => injected ?? browserController());
  const model = useSyncExternalStore(controller.subscribe, controller.getSnapshot, controller.getSnapshot);
  useEffect(() => {
    void controller.start();
  }, [controller]);
  const store = useMemo(
    () => createWorkspaceStore(model.activeId === null ? browserDeps() : controller.workspaceDeps(model.activeId)),
    [controller, model.activeId],
  );
  return (
    <LiveContext.Provider value={{ controller, model }}>
      <WorkspaceProvider store={store} autoStart={model.activeId !== null}>
        {children}
      </WorkspaceProvider>
    </LiveContext.Provider>
  );
}
