import { governanceCounts } from "../core/filters";
import { countReading, type Reading } from "./panels";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Shell.module.css";

// The footer metrics. A metric the projection does not carry is "unavailable" with its
// reason, never a zero. The policy counts describe the retained window only.
export function footerMetrics(model: Pick<WorkspaceModel, "session" | "items" | "evicted">): Array<readonly [string, Reading]> {
  const counts = governanceCounts(model.items);
  const evicted = model.evicted > 0 ? ` · ${model.evicted} earlier evicted` : "";
  return [
    ["Turns", countReading(model.session?.turn_count, "no session projection")],
    ["Records retained", { kind: "value", text: `${model.items.length}${evicted}` }],
    [
      "Policy, retained window",
      { kind: "value", text: `ALLOW ${counts.allow} · DENY ${counts.deny} · UNKNOWN ${counts.unknownDecisions}` },
    ],
    ["Token usage", { kind: "unavailable", reason: "the projection records no token usage" }],
    ["Duration", { kind: "unavailable", reason: "the projection records no duration" }],
  ];
}

export function Footer({ model, version }: { readonly model: WorkspaceModel; readonly version: string }) {
  return (
    <footer aria-label="Observed session metrics" className={styles.footer}>
      <dl className={styles.metrics}>
        {footerMetrics(model).map(([label, reading]) => (
          <div key={label} className={styles.metric}>
            <dt>{label}</dt>
            <dd>
              {reading.kind === "value" ? (
                reading.text
              ) : (
                <>
                  unavailable <span className={styles.reason}>({reading.reason})</span>
                </>
              )}
            </dd>
          </div>
        ))}
      </dl>
      <p className={styles.build}>build {version}</p>
    </footer>
  );
}
