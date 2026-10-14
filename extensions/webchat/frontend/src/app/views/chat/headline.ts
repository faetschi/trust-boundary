// The headline of a tool card: the summary it shows after the tool name. The card draws the name once,
// so a summary that repeats the name at its start is trimmed to what it adds. Pure: no DOM, no React.

import { EMPTY_TOOL_SUMMARY } from "./readings";

// What may separate the repeated name from the rest. A hyphen is not one, so "bash -c" keeps its flag.
const SEPARATOR = /^[\s:·—–]/;
const LEADING_SEPARATORS = /^[\s:·—–]+/;

// The text after the tool name. A blank or absent summary reads as no bounded summary. A summary that
// is only the name reads as nothing, because the card already shows the name.
export function headlineSummary(tool: string, summary: string | undefined): string {
  const text = (summary ?? "").trim().normalize("NFC");
  if (text === "") return EMPTY_TOOL_SUMMARY;
  const name = tool.trim().normalize("NFC");
  if (name === "") return text;
  const folded = name.toLowerCase();
  let rest = text;
  // Each pass removes one repeat and at least one separator, so the loop ends.
  for (;;) {
    if (rest.toLowerCase() === folded) return "";
    if (rest.slice(0, name.length).toLowerCase() !== folded) return rest;
    const after = rest.slice(name.length);
    // The name must end at a word boundary: "readme" is not a repeat of "read".
    if (!SEPARATOR.test(after)) return rest;
    rest = after.replace(LEADING_SEPARATORS, "");
  }
}
