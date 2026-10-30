import { createContext, useContext, useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { AnnouncerContext } from "../ui/LiveRegion";
import { browserDeps } from "./browser";
import { createWorkspaceStore, type WorkspaceDeps, type WorkspaceModel, type WorkspaceStore } from "./workspaceStore";

const WorkspaceContext = createContext<WorkspaceStore | null>(null);

interface WorkspaceProviderProps {
  // Injected facilities. Omitted in the app, where the browser versions are used.
  readonly deps?: WorkspaceDeps;
  // A store the caller owns and replaces (live mode swaps it when another session opens). The children stay mounted, so a
  // swap keeps focus and layout state, and the provider stops the old store and starts the new one.
  readonly store?: WorkspaceStore;
  // False until the page knows which gateway it talks to (App.tsx). The store then stays unstarted and reads "loading".
  readonly autoStart?: boolean;
  readonly children: ReactNode;
}

// Owns one workspace store for the life of the page, or runs the one it is given. The stream opens in an effect (snapshot
// first), closes on unmount, and reopens on a StrictMode remount, because start and stop
// are both repeatable.
export function WorkspaceProvider({ deps, store: given, autoStart = true, children }: WorkspaceProviderProps) {
  const [owned] = useState(() => (given ? null : createWorkspaceStore(deps ?? browserDeps())));
  const store = given ?? owned;
  if (store === null) throw new Error("WorkspaceProvider: the store prop was removed after it was given");
  useEffect(() => {
    if (autoStart) store.start();
    const online = () => store.goOnline();
    const offline = () => store.goOffline();
    window.addEventListener("online", online);
    window.addEventListener("offline", offline);
    return () => {
      window.removeEventListener("online", online);
      window.removeEventListener("offline", offline);
      store.stop();
    };
  }, [store, autoStart]);
  return (
    <WorkspaceContext.Provider value={store}>
      <AnnouncerContext.Provider value={store.announcer}>{children}</AnnouncerContext.Provider>
    </WorkspaceContext.Provider>
  );
}

// The current immutable workspace model. Re-renders only when the store publishes a change.
export function useWorkspace(): WorkspaceModel {
  const store = useContext(WorkspaceContext);
  if (!store) throw new Error("useWorkspace: render inside a WorkspaceProvider");
  return useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot);
}
