import { Shell } from "./app/Shell";
import { WorkspaceProvider } from "./app/WorkspaceProvider";

export interface AppProps {
  // The build version, injected at build time from package.json.
  readonly version: string;
}

// The product root: the workspace store and the shell. The vanilla UI at / is unaffected;
// this client is served under /app/.
export function App({ version }: AppProps) {
  return (
    <WorkspaceProvider>
      <Shell version={version} />
    </WorkspaceProvider>
  );
}
