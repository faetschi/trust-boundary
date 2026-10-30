import { useEffect, useState } from "react";
import { browserLiveDeps } from "./app/browser";
import { Shell } from "./app/Shell";
import { WorkspaceProvider } from "./app/WorkspaceProvider";
import type { LiveController } from "./live/controller";
import { detectGateway, type GatewayMode } from "./live/gateway";
import { LiveRoot } from "./live/LiveRoot";
import { Unpaired } from "./live/Unpaired";

export interface AppProps {
  // The build version, injected at build time from package.json.
  readonly version: string;
  // Which gateway served the page. The app asks GET /api/v1/contract; tests inject the answer.
  readonly detect?: () => Promise<GatewayMode>;
  // The live controller. The app builds the browser one; tests inject one over a fake fetch.
  readonly controller?: LiveController;
}

const detectInBrowser = () => detectGateway(browserLiveDeps().fetch);

// The product root: the workspace store and the shell. This client is served under /app/. A live gateway (--live) gets the
// live shell, with its sessions, composer and review. Any other gateway, and anything the page cannot read, gets the
// read-only demonstration path as before. Until the gateway is known the shell is drawn with an unstarted store, which
// reads "loading", so the first paint is the same either way.
export function App({ version, detect = detectInBrowser, controller }: AppProps) {
  const [mode, setMode] = useState<GatewayMode | "detecting">("detecting");
  useEffect(() => {
    let current = true;
    void detect().then((found) => {
      if (current) setMode(found);
    });
    return () => {
      current = false;
    };
  }, [detect]);

  if (mode === "unpaired") return <Unpaired />;
  if (mode === "live") {
    return (
      <LiveRoot controller={controller}>
        <Shell version={version} />
      </LiveRoot>
    );
  }
  return (
    <WorkspaceProvider autoStart={mode === "demo"}>
      <Shell version={version} />
    </WorkspaceProvider>
  );
}
