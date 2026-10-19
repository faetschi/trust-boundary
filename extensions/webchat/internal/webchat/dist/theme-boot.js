// Applies the stored theme choice before the first paint. It is a classic script, not a module, and index.html loads it
// first in <head>: the parser waits for it, so the page is first painted with the stored choice, even when that
// differs from the OS scheme. It sets data-theme exactly as src/theme/theme.ts does, applyTheme(readTheme(storage)).
// Dark is the default: nothing stored, an unknown value, or withheld storage all set data-theme="dark".
// src/theme/boot.test.ts runs this file and checks that the two agree. Keep this file plain: no import or export, no
// inline style, and no inline script anywhere (the policy is script-src 'self').
(function () {
  var choice = "dark";
  try {
    var stored = window.localStorage.getItem("tbound-webchat-theme");
    if (stored === "light" || stored === "dark" || stored === "system") choice = stored;
  } catch (error) {
    // Storage throws or is withheld: the dark default stands, as it does with nothing stored.
  }
  if (choice === "system") {
    document.documentElement.removeAttribute("data-theme");
  } else {
    document.documentElement.setAttribute("data-theme", choice);
  }
})();
