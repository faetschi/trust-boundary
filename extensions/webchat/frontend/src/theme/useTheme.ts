import { useCallback, useState } from "react";
import { applyTheme, readTheme, storageOf, writeTheme, type ThemeChoice } from "./theme";

// The theme choice for the toggle. The attribute is applied at once, and the choice is
// persisted when storage allows it. A refused write does not block the change.
export function useTheme(): readonly [ThemeChoice, (choice: ThemeChoice) => void] {
  const [choice, setChoiceState] = useState<ThemeChoice>(() => readTheme(storageOf(window)));
  const setChoice = useCallback((next: ThemeChoice) => {
    applyTheme(document.documentElement, next);
    writeTheme(storageOf(window), next);
    setChoiceState(next);
  }, []);
  return [choice, setChoice];
}
