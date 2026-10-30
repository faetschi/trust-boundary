import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const shell = readFileSync(new URL("../assets/index.html", import.meta.url), "utf8");

// openingTag returns the start tag of the element with the given id, so attribute
// checks cannot match an attribute that belongs to a neighbouring element.
function openingTag(id) {
  const match = new RegExp(`<[a-z]+\\s+id="${id}"[^>]*>`).exec(shell);
  assert.ok(match, `#${id} must exist in the shell`);
  return match[0];
}

test("the timeline filter and the trajectory grouping are toolbars with a label", () => {
  const filters = openingTag("timeline-filters");
  assert.match(filters, /role="toolbar"/);
  assert.match(filters, /aria-label="Filter timeline"/);
  const groups = openingTag("trajectory-groups");
  assert.match(groups, /role="toolbar"/);
  assert.match(groups, /aria-label="Group records by"/);
});

test("the shell declares no on/off switch: every control is a button or a tab", () => {
  assert.doesNotMatch(shell, /role="switch"/);
});

test("each view has a text-labelled jump control, hidden until records arrive below a scrolled reader", () => {
  for (const id of ["chat-jump", "trajectory-jump"]) {
    const tag = openingTag(id);
    assert.match(tag, /type="button"/);
    assert.match(tag, /\bhidden\b/, `${id} starts hidden`);
  }
});

test("the ledger is a focus target for the jump control, as the transcript already is", () => {
  assert.match(openingTag("ledger"), /tabindex="-1"/);
  assert.match(openingTag("timeline"), /tabindex="-1"/);
});
