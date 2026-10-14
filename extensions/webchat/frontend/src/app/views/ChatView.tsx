import { EmptyState } from "../../ui/StateMessage";
import type { WorkspaceModel } from "../workspaceStore";

// Phase 3b placeholder. Agent Q replaces this body with the transcript, the posture and the
// filter toolbar. It already receives the typed model, so it needs no new plumbing.
export function ChatView({ model }: { readonly model: WorkspaceModel }) {
  return (
    <EmptyState title="Chat transcript: Phase 3b">
      <p>The transcript renders here in Phase 3b. Retained records: {model.items.length}.</p>
    </EmptyState>
  );
}
