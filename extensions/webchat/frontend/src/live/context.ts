import { createContext, useContext } from "react";
import type { LiveController, LiveModel } from "./controller";

// What the live components read: the controller to ask, and the model it last published. It is null in the demonstration
// modes, so a component that renders there takes its read-only form.
export interface LiveBinding {
  readonly controller: LiveController;
  readonly model: LiveModel;
}

export const LiveContext = createContext<LiveBinding | null>(null);

export function useLive(): LiveBinding | null {
  return useContext(LiveContext);
}
