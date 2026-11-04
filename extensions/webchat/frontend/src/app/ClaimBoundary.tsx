import { Chip } from "../ui/Chip";
import { Notice } from "../ui/Notice";
import { isVerifiedProjection } from "../core/format";
import { NO_PROJECTION, NO_QUALIFICATION, qualificationRows, unavailableWords } from "./panels";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Shell.module.css";

export const DISPLAY_ONLY_TEXT = "Display only: these values are copied from the projection and are not evidence.";
export const UNVERIFIED_CLAIM_TEXT = "unverified projection claims true";

// The two rows that claim qualification (labels from panels.ts qualificationRows). A true value from a
// projection that is not verified is a claim the page cannot back, so it carries a warning and never a pass.
const CLAIM_ROWS: ReadonlySet<string> = new Set(["G1", "Claim-bearing"]);

function qualificationOf(model: WorkspaceModel) {
  const panels = model.panels;
  return qualificationRows(panels?.qualification, panels ? NO_QUALIFICATION : NO_PROJECTION);
}

// Whether the projection claims a true that nothing verifies. The status pill in the header says so on its face, so the
// warning is never only inside the popover.
export function claimsUnverifiedTrue(model: WorkspaceModel): boolean {
  if (isVerifiedProjection(model)) return false;
  return qualificationOf(model).some(([label, reading]) => CLAIM_ROWS.has(label) && reading.kind === "value" && reading.text === "true");
}

// The claim boundary. It is the body of the status pill's popover (StatusPill.tsx): every value is the projection's own,
// and before a projection loads, or when a value is absent, it reads unavailable with its reason, never a literal copied
// into the page. The two sentences that say what the values are stay with the values.
export function ClaimBoundary({ model }: { readonly model: WorkspaceModel }) {
  const verified = isVerifiedProjection(model);
  return (
    <Notice label="Claim boundary" tone="claim">
      <dl className={styles.claim}>
        {qualificationOf(model).map(([label, reading]) => {
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
      <div className={styles.explain}>
        <p className={styles.claimText}>Presentation only: the supervisor retains policy, effect and publication authority.</p>
        <p className={styles.claimText}>{DISPLAY_ONLY_TEXT}</p>
      </div>
    </Notice>
  );
}
