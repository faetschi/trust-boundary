// Applies the stored theme choice before the first React render. main.tsx imports this
// module first, so the attribute is set while the document is still being evaluated.
// The CSP forbids an inline script, so this bundled module is the earliest hook. With
// no stored choice, or the system choice, tokens.css' prefers-color-scheme query alone
// decides, and no script is needed.
import { applyTheme, readTheme, storageOf } from "./theme";

applyTheme(document.documentElement, readTheme(storageOf(window)));
