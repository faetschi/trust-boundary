// Re-applies the stored theme choice when the React bundle runs. The first paint does not
// wait for this module. index.html loads public/theme-boot.js first, a classic script in
// <head>, and that script has already set data-theme before the page is painted. A module
// script is deferred until after parsing, so it is not the first-paint hook. This module is
// the fallback that keeps the attribute in step with storage when the boot script did not
// run, and it changes nothing when the boot script did.
import { applyTheme, readTheme, storageOf } from "./theme";

applyTheme(document.documentElement, readTheme(storageOf(window)));
