// Port of internal/webchat/jstest/format.test.mjs. Same test names and assertions.
import { test } from "vitest";
import assert from "node:assert/strict";

import { shortDigest, slug, stateChipClass, verdictChipClass } from "./format";
import { isItemEvidenceOnly } from "./verification";

test("unverified success is downgraded to a reported chip", () => {
  const fixtureResult = { authority: "diagnostic", verification_scope: "unverified-input", state: "succeeded" };
  assert.equal(isItemEvidenceOnly(fixtureResult), false);
  assert.equal(stateChipClass("succeeded", isItemEvidenceOnly(fixtureResult)), "reported");
  assert.equal(stateChipClass("completed", isItemEvidenceOnly(fixtureResult)), "reported");
});

test("only a non-diagnostic, non-unverified item is treated as verified", () => {
  const live = { authority: "observed", verification_scope: "structural-audit-only", state: "succeeded" };
  assert.equal(isItemEvidenceOnly(live), true);
  assert.equal(stateChipClass("succeeded", isItemEvidenceOnly(live)), "succeeded");
});

test("the verification gate fails closed", () => {
  // A missing or unexpected scope must never read as verified, and the observed
  // authority is required alongside the structural-audit scope.
  assert.equal(isItemEvidenceOnly({ authority: "unknown", verification_scope: "", state: "succeeded" }), false);
  assert.equal(isItemEvidenceOnly({ authority: "observed", verification_scope: "", state: "succeeded" }), false);
  assert.equal(isItemEvidenceOnly({ authority: "observed", verification_scope: "unavailable", state: "succeeded" }), false);
  assert.equal(isItemEvidenceOnly({ authority: "diagnostic", verification_scope: "structural-audit-only", state: "succeeded" }), false);
  assert.equal(isItemEvidenceOnly({ authority: "authoritative", verification_scope: "structural-audit-only", state: "succeeded" }), false);
  assert.equal(stateChipClass("succeeded", isItemEvidenceOnly({ authority: "observed", verification_scope: "" })), "reported");
});

test("negative and unknown states are never restyled as success", () => {
  assert.equal(stateChipClass("denied", true), "denied");
  assert.equal(stateChipClass("failed", true), "failed");
  assert.equal(stateChipClass("unknown", true), "unknown");
  assert.equal(stateChipClass("lost", true), "lost");
  assert.equal(stateChipClass("unknown", false), "unknown");
});

test("verdict chips map to a closed set", () => {
  assert.equal(verdictChipClass("ALLOW"), "allow");
  assert.equal(verdictChipClass("DENY"), "deny");
  assert.equal(verdictChipClass("WEIRD"), "unknown");
});

test("slug sanitizes arbitrary values", () => {
  assert.equal(slug("SUCCEEDED<script>alert(1)</script>"), "succeeded-script-alert-1-script");
  assert.equal(slug(""), "unknown");
  assert.equal(slug(null), "unknown");
});

test("shortDigest bounds long references", () => {
  const digest = "sha256:" + "a".repeat(64);
  const short = shortDigest(digest);
  assert.ok(short.length < digest.length);
  assert.ok(short.includes("…"));
  assert.equal(shortDigest("sha256:abc"), "sha256:abc");
  assert.equal(shortDigest(undefined), "—");
});
