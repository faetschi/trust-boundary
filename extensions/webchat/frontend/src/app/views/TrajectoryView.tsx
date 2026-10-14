import { EmptyState } from "../../ui/StateMessage";
import type { WorkspaceModel } from "../workspaceStore";

// Phase 3c placeholder. Agent R replaces this body with the record ledger, the sequence rail
// and the search. Selecting a record goes through useShell().selectRecord(id, row), so the
// focus contract in shellContext.ts applies unchanged.
export function TrajectoryView({ model }: { readonly model: WorkspaceModel }) {
  return (
    <EmptyState title="Trajectory ledger: Phase 3c">
      <p>The record ledger renders here in Phase 3c. Retained records: {model.items.length}.</p>
    </EmptyState>
  );
}
