// The browser facilities the workspace needs, injected into the core. This is the only
// module that reads window, fetch, EventSource, timers or navigator. Tests replace all of it.
import type { WorkspaceDeps } from "./workspaceStore";

export function browserDeps(win: Window & typeof globalThis = window): WorkspaceDeps {
  return {
    // The signal is always one this app's AbortController made, so the cast to the browser's
    // AbortSignal is exact. The core reads only `aborted` and passes the signal on.
    fetch: (url, init) => win.fetch(url, { ...init, signal: init.signal as AbortSignal | undefined }),
    openSource: (url) => {
      const source = new win.EventSource(url);
      return {
        addEventListener(type, listener) {
          source.addEventListener(type, (event: Event) => {
            const data = (event as MessageEvent).data;
            listener({ data: typeof data === "string" ? data : undefined });
          });
        },
        close() {
          source.close();
        },
      };
    },
    isOnline: () => win.navigator.onLine,
    // The handle is opaque to the core. It is a number here, as window.setTimeout returns.
    setTimer: (callback, delayMs) => win.setTimeout(callback, delayMs),
    clearTimer: (handle) => win.clearTimeout(handle as number),
    createAbortController: () => new win.AbortController(),
    nowMs: () => Date.now(),
  };
}
