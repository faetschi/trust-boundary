import { EmptyState } from "../../ui/StateMessage";
import type { Item } from "../../core/contract";

// Phase 3c slot. The inspector shell gives it the selected record and owns the close button
// and focus return (see shellContext.ts). Agent R fills this body.
export function RecordInspector({ record }: { readonly record: Item }) {
  return (
    <EmptyState title="Record inspector: Phase 3c">
      <p>
        Record {record.sequence} ({record.type}). Its bounded fields render here in Phase 3c.
      </p>
    </EmptyState>
  );
}
