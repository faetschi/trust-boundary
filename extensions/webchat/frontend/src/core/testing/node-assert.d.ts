// Test-only typings for the subset of `node:assert/strict` the parity tests use. The project
// has no @types/node and adds no dependency for it, so this ambient declaration is the whole
// surface. It describes only what the tests call, and at runtime Vitest runs the real module.
// This file has no top-level import or export, so it stays an ambient script.
declare module "node:assert/strict" {
  interface Assert {
    ok(value: unknown, message?: string | Error): asserts value;
    equal(actual: unknown, expected: unknown, message?: string | Error): void;
    notEqual(actual: unknown, expected: unknown, message?: string | Error): void;
    deepEqual(actual: unknown, expected: unknown, message?: string | Error): void;
    doesNotThrow(block: () => unknown, message?: string | Error): void;
    match(value: string, pattern: RegExp, message?: string | Error): void;
    doesNotMatch(value: string, pattern: RegExp, message?: string | Error): void;
    rejects(block: Promise<unknown>, error?: unknown, message?: string | Error): Promise<void>;
  }
  const assert: Assert;
  export default assert;
}
