// Applies the stored theme choice before the first paint. It is a classic script, not a module, and index.html loads it
// first in <head>: the parser waits for it, so the page is first painted with the stored choice, even when that
// differs from the OS scheme. It sets data-theme exactly as src/theme/theme.ts does, applyTheme(readTheme(storage)).
// src/theme/boot.test.ts runs this file and checks that the two agree. Storage may throw or be withheld; then the
// system choice stands. Keep this file plain: no import or export, no inline style, and no inline script anywhere
// (the policy is script-src 'self').
(function () {
  try {
    var choice = window.localStorage.getItem("tbound-webchat-theme");
    if (choice === "light" || choice === "dark") {
      document.documentElement.setAttribute("data-theme", choice);
    }
  } catch (error) {
    // Storage throws or is withheld: the system choice stands, as it does with nothing stored.
  }
})();
