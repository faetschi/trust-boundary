import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { test } from "node:test";
import {
  GOVERNED_PI_DESCRIPTOR_MAP,
  GOVERNED_PI_WORKER_VERSION,
  validateGovernedPiBootstrap,
  type GovernedPiWorkerPreflight,
  type WorkerFileIdentity,
} from "./governed-pi-worker.ts";

const SCHEMAS = {
  read: "5fad0fa7493528bc4284f5c447781325f67099f5fc018206efdc461d33962e85",
  write: "7e51d1de0f2ccb5fe8de82d51ee3a1d9065e3c4f7c53497ce12f283d6f2f5aa0",
  edit: "394741d0cb4c9a6fe96d3275897405c8b13a1c7ff712426a942c10d484993fa1",
  bash: "854beb37435894c8f97dcad8d4610c0ea1458b57a6a8d4bb5b0ea435cdaa5893",
} as const;

function canonicalEnvironmentDigest(environment: Record<string, string>): string {
  const value = Object.keys(environment).sort().map((name) => `${name}=${environment[name]}`).sort().join("\0");
  return `sha256:${createHash("sha256").update(value).digest("hex")}`;
}

function stat(options: Partial<WorkerFileIdentity> = {}): WorkerFileIdentity {
  return {
    dev: 10,
    ino: 20,
    mode: 0o40700,
    uid: 1000,
    isDirectory: () => true,
    isFile: () => true,
    isSymbolicLink: () => false,
    ...options,
  };
}

function fixture() {
  const environment = {
    HOME: "/private/home",
    LANG: "C.UTF-8",
    PI_OFFLINE: "1",
    TMPDIR: "/private/tmp",
  };
  const preflight: GovernedPiWorkerPreflight = {
    environment,
    nodeVersion: "v24.15.0",
    cwd: "/private/session",
    uid: 1000,
    cwdDescriptor: stat(),
    cwdPath: stat(),
    bundleDescriptor: stat({ dev: 11, ino: 21 }),
    bundlePath: stat({ dev: 11, ino: 21 }),
    executableDescriptor: stat({ mode: 0o100755, isDirectory: () => false }),
    workerEntryDescriptor: stat({ dev: 11, ino: 22, mode: 0o100644, isDirectory: () => false }),
    argvWorkerEntry: stat({ dev: 11, ino: 22, mode: 0o100644, isDirectory: () => false }),
    argvWorkerPath: "/proc/self/fd/9/src/governed-pi-worker.ts",
    homePath: stat(),
    tempPath: stat(),
  };
  const digest = "sha256:" + "a".repeat(64);
  const bootstrap = {
    schema_version: GOVERNED_PI_WORKER_VERSION,
    profile_id: "dev-profile",
    profile_digest: digest,
    source_binding_digest: digest,
    source_provenance_digest: digest,
    runtime_bundle_digest: digest,
    descriptor_profile: "tbound-pi-sdk-fdmap/v1;exec=3;cwd=4;bootstrap=5;ipc=6;provider=7;events=8;bundle=9;control=0",
    descriptor_profile_digest: digest,
    child_environment_digest: canonicalEnvironmentDigest(environment),
    node_version: "v24.15.0",
    session_id: "session-1",
    workflow_id: "workflow-1",
    conversation_id: "conversation-1",
    private_cwd: "/private/session",
    ipc_binding_token: "ab".repeat(32),
    provider_id: "tbound-go-broker",
    model_id: "nvidia/nemotron-3.5-lightning:free",
    pi_version: "0.87.1",
    tools: ["read", "write", "edit", "bash"],
    tool_schema_sha256: { ...SCHEMAS },
  };
  return { bootstrap, preflight };
}

test("governed Pi worker accepts only its bound descriptor/profile/environment plan", () => {
  assert.deepEqual(GOVERNED_PI_DESCRIPTOR_MAP, {
    executable: 3,
    working_directory: 4,
    bootstrap: 5,
    proposal_ipc: 6,
    provider_bridge: 7,
    worker_events: 8,
    runtime_bundle: 9,
    session_control: 0,
  });
  const { bootstrap, preflight } = fixture();
  assert.equal(validateGovernedPiBootstrap(bootstrap, preflight).profile_id, "dev-profile");
});

test("governed Pi worker refuses extra env authority and descriptor/tool/profile drift", () => {
  const { bootstrap, preflight } = fixture();
  assert.throws(
    () => validateGovernedPiBootstrap(bootstrap, {
      ...preflight,
      environment: { ...preflight.environment, OPENAI_API_KEY: "synthetic-sentinel" },
    }),
    /minimal offline allowlist/,
  );
  assert.throws(
    () => validateGovernedPiBootstrap({ ...bootstrap, descriptor_profile: "caller-selected-fd-map" }, preflight),
    /fixed admitted Pi profile/,
  );
  assert.throws(
    () => validateGovernedPiBootstrap({ ...bootstrap, tool_schema_sha256: { ...bootstrap.tool_schema_sha256, bash: "0".repeat(64) } }, preflight),
    /schema profile/,
  );
  assert.throws(
    () => validateGovernedPiBootstrap(bootstrap, { ...preflight, cwdPath: stat({ ...preflight.cwdPath, ino: 99 }) }),
    /descriptor does not match/,
  );
  assert.throws(
    () => validateGovernedPiBootstrap({ ...bootstrap, unbound_authority: true }, preflight),
    /fixed admitted Pi profile/,
  );
});
