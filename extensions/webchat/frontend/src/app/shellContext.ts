import { createContext, useContext } from "react";

// What the views and the record inspector may ask of the shell. This is the focus contract:
//   - selectRecord(id, row) selects a record. At narrow widths it opens the drawer and moves
//     focus into it. `row` is the element to return focus to when the drawer closes.
//   - clearRecord() clears the selection. At narrow widths it closes the drawer and returns
//     focus to the row (or to the drawer toggle when no row was given).
//   - openInspector / closeInspector are the drawer alone, for the toggle and the Escape key.
export interface ShellControls {
  // The viewport is at or below the drawer breakpoint.
  readonly narrow: boolean;
  // The drawer is open. Always false at wide widths, where the inspector is a column.
  readonly drawerOpen: boolean;
  openInspector(returnTo?: HTMLElement | null): void;
  closeInspector(): void;
  selectRecord(id: string, returnTo?: HTMLElement | null): void;
  clearRecord(): void;
}

export const ShellContext = createContext<ShellControls | null>(null);

export function useShell(): ShellControls {
  const controls = useContext(ShellContext);
  if (!controls) throw new Error("useShell: render inside the Shell");
  return controls;
}
