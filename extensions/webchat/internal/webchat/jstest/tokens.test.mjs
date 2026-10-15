import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// Comments are removed first: a comment before a rule would otherwise become part
// of that rule's selector and hide the rule from the checks below.
const css = readFileSync(new URL("../assets/styles.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

// The state colour tiers: static palette tokens feed the state aliases, and only
// the aliases feed the chip, dot and segment components.
const rootBlock = /:root\s*\{([^}]*)\}/.exec(css);
assert.ok(rootBlock, ":root must declare the palette");
const rootTokens = new Map();
for (const match of rootBlock[1].matchAll(/--([a-z0-9-]+)\s*:\s*([^;]+);/g)) rootTokens.set(match[1], match[2].trim());

test("every state alias is declared in :root and names a palette token or a literal", () => {
  const aliases = [...rootTokens.keys()].filter((name) => name.startsWith("state-"));
  for (const name of ["state-ok", "state-allow", "state-deny", "state-reported", "state-pending", "state-unknown", "state-idle"]) {
    assert.ok(aliases.includes(name), `--${name} is declared`);
  }
  for (const name of aliases) {
    const value = rootTokens.get(name);
    assert.ok(value, `--${name} has a value`);
  }
});

test("state chips, dots and segments read only the state aliases, never the palette", () => {
  const offenders = [];
  for (const rule of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = rule[1].split(",").map((part) => part.trim());
    // Source chips carry provenance colour, not state colour, and are out of this tier.
    const stateBearing = selectors.some((selector) =>
      /^\.(chip|dot|seg)-/.test(selector) && !selector.startsWith(".chip-source-"),
    );
    if (!stateBearing) continue;
    for (const reference of rule[2].matchAll(/var\(--([a-z0-9-]+)/g)) {
      if (!reference[1].startsWith("state-")) offenders.push(`${selectors.join(", ")} reads --${reference[1]}`);
    }
  }
  assert.deepEqual(offenders, [], "state components must read only --state-* aliases");
});
