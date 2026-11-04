import { Chip } from "../ui/Chip";
import { Disclosure } from "../ui/Disclosure";
import { Notice } from "../ui/Notice";
import { isVerifiedProjection } from "../core/format";
import { NO_PROJECTION, NO_QUALIFICATION, qualificationRows, unavailableWords } from "./panels";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Shell.module.css";

export const DISPLAY_ONLY_TEXT = "Display only: these values are copied from the projection and are not evidence.";
export const UNVERIFIED_CLAIM_TEXT = "unverified projection claims true";
// The header folds the two explanatory sentences under this summary, at every width. The claim values never fold.
const EXPLANATION_SUMMARY = "Presentation only · display only";

// The two rows that claim qualification (labels from panels.ts qualificationRows). A true value from a
// projection that is not verified is a claim the page cannot back, so it carries a warning and never a pass.
const CLAIM_ROWS: ReadonlySet<string> = new Set(["G1", "Claim-bearing"]);

// The claim boundary of the header. Every value is the projection's own: before a projection loads, or
// when a value is absent, it reads unavailable with its reason, never a literal copied into the page.
export function ClaimBoundary({ model }: { readonly model: WorkspaceModel }) {
  const verified = isVerifiedProjection(model);
  const panels = model.panels;
  const rows = qualificationRows(panels?.qualification, panels ? NO_QUALIFICATION : NO_PROJECTION);
  const explanation = (
    <div className={styles.explain}>
      <p className={styles.claimText}>Presentation only: the supervisor retains policy, effect and publication authority.</p>
      <p className={styles.claimText}>{DISPLAY_ONLY_TEXT}</p>
    </div>
  );
  // The claim values come first, then the folded explanation on the same line where it fits (the flex row in Shell.module.css).
  return (
    <Notice label="Claim boundary" tone="claim">
      <dl className={styles.claim}>
        {rows.map(([label, reading]) => {
          const unverifiedTrue = !verified && CLAIM_ROWS.has(label) && reading.kind === "value" && reading.text === "true";
          return (
            <div key={label}>
              <dt>{label}</dt>
              <dd>
                <span>{reading.kind === "value" ? reading.text : unavailableWords(reading.reason)}</span>
                {unverifiedTrue ? <Chip tone="unknown">{UNVERIFIED_CLAIM_TEXT}</Chip> : null}
              </dd>
            </div>
          );
        })}
      </dl>
      <Disclosure summary={EXPLANATION_SUMMARY}>{explanation}</Disclosure>
    </Notice>
  );
}
