// filters.mjs — pure, DOM-free timeline filtering for governance review.
//
// The primary review question for a governed run is "what was not a clean
// allow?". These predicates keep a correlated tool block together (activity +
// decision + result) so a matched finding is never shown without its execution
// context, and they never relabel an UNKNOWN as a success.

export const FILTERS = [
  { id: "all", label: "All", hint: "every projected element" },
  { id: "denied", label: "Denied", hint: "DENY decisions and denied results" },
  { id: "unknown", label: "Unknown", hint: "UNKNOWN decisions, results and LOSSY losses" },
  { id: "failed", label: "Failed", hint: "failed results" },
];

export function matchesFilter(item, filter) {
  if (!item) return false;
  switch (filter) {
    case "denied":
      return (item.type === "decision" && item.verdict === "DENY") || item.state === "denied";
    case "unknown":
      return (
        (item.type === "decision" && item.verdict === "UNKNOWN") ||
        item.state === "unknown" ||
        item.state === "lost"
      );
    case "failed":
      return item.state === "failed";
    default:
      return true;
  }
}

export function countFilter(items, filter) {
  if (filter === "all") return items.length;
  let count = 0;
  for (const item of items) {
    if (matchesFilter(item, filter)) count += 1;
  }
  return count;
}

// filterItems returns the elements to render. With the default filter it is the
// identity. Otherwise a tool block is retained when any of its correlated items
// matches, and all of that block's items are kept so the reader keeps context.
// Transcript messages are omitted while a finding filter is active.
export function filterItems(items, filter) {
  if (!filter || filter === "all") return items.slice();
  const matched = new Set();
  for (const item of items) {
    if (matchesFilter(item, filter)) matched.add(item.tool_call_id || item.id);
  }
  const kept = [];
  for (const item of items) {
    if (item.type === "message") continue;
    if (matched.has(item.tool_call_id || item.id)) kept.push(item);
  }
  return kept;
}

// governanceCounts summarises the observed posture. It counts what was
// observed; it is not a policy correctness claim. A successful execution is
// split into "verified" and "reported" so an unverified success is never
// presented with the confirmed-success styling.
export function governanceCounts(items, isVerifiedItem) {
  const counts = {
    allow: 0,
    deny: 0,
    unknownDecisions: 0,
    verified: 0,
    reported: 0,
    denied: 0,
    failed: 0,
    unknownResults: 0,
    lost: 0,
  };
  for (const item of items) {
    if (item.type === "decision") {
      if (item.verdict === "ALLOW") counts.allow += 1;
      else if (item.verdict === "DENY") counts.deny += 1;
      else counts.unknownDecisions += 1;
    }
    if (item.type === "result") {
      switch (item.state) {
        case "succeeded":
          if (typeof isVerifiedItem === "function" && isVerifiedItem(item)) counts.verified += 1;
          else counts.reported += 1;
          break;
        case "denied":
          counts.denied += 1;
          break;
        case "failed":
          counts.failed += 1;
          break;
        case "lost":
          counts.lost += 1;
          break;
        default:
          counts.unknownResults += 1;
      }
    }
  }
  return counts;
}
