import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { Type } from "typebox";
import { createProxyParameters, createProxyTools, declaredToolNames } from "./proxy-tools.ts";

function sortedJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(sortedJson).join(",")}]`;
  if (value !== null && typeof value === "object") {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${sortedJson(record[key])}`).join(",")}}`;
  }
  return JSON.stringify(value);
}

// profile.json pins the digest of each registered schema. The broker advertises the same schemas
// (supervisor/internal/broker/adapter_surface_test.go) and the worker echoes the same pins
// (supervisor/internal/piruntime), so an edit to a schema has to change all of them together.
test("the registered tool schemas hash to the digests adapter/profile.json pins", () => {
  const profile = JSON.parse(readFileSync(new URL("../profile.json", import.meta.url), "utf8")) as {
    schema_sha256: Record<string, string>;
  };
  const parameters = createProxyParameters(Type);
  for (const name of declaredToolNames) {
    const digest = createHash("sha256").update(sortedJson(parameters[name])).digest("hex");
    assert.equal(digest, profile.schema_sha256[name], `${name} schema digest`);
  }
});

test("the tool schemas advertise only what the durable executor does", () => {
  const parameters = createProxyParameters(Type);
  const properties = (name: (typeof declaredToolNames)[number]) => Object.keys(parameters[name].properties ?? {}).sort();
  assert.deepEqual(properties("read"), ["path"]);
  assert.deepEqual(properties("bash"), ["command"]);
  const tools = createProxyTools({ send: () => Promise.reject(new Error("unused")) }, Type);
  const bash = tools.find((tool) => tool.name === "bash");
  // The adapter serves serve --pi, whose executor returns the exit code alone, so it promises no output.
  // A product turn's request, built by the broker, adds the output text (supervisor/internal/broker).
  assert.equal(bash?.description, "Execute a bash command. Requests are forwarded to tbound.");
  const read = tools.find((tool) => tool.name === "read");
  assert.match(read?.description ?? "", /UTF-8 text file/);
  assert.doesNotMatch(read?.description ?? "", /Supports text files and images/);
});
