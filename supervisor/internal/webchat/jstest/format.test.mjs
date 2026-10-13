import test from "node:test";
import assert from "node:assert/strict";

import {
  isVerified,
  shortDigest,
  slug,
  stateChipClass,
  verdictChipClass,
} from "../assets/format.mjs";

test("unverified success is downgraded to a reported chip", () => {
  const fixtureResult = { authority: "diagnostic", verification_scope: "unverified-input", state: "succeeded" };
  assert.equal(isVerified(fixtureResult), false);
  assert.equal(stateChipClass("succeeded", isVerified(fixtureResult)), "reported");
  assert.equal(stateChipClass("completed", isVerified(fixtureResult)), "reported");
});

test("only a non-diagnostic, non-unverified item is treated as verified", () => {
  const live = { authority: "observed", verification_scope: "structural-audit-only", state: "succeeded" };
  assert.equal(isVerified(live), true);
  assert.equal(stateChipClass("succeeded", isVerified(live)), "succeeded");
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
