# Slice 4: reviewed guest offline verifier blocked (2026-10-05)

**No current-source offline verification report or manifest exists from this
execution. Historical reports do not verify the new source.**

## Reviewed prerequisites and independent checks

The existing untracked `infra/hyperv/Invoke-TBoundTrustedOfflineVerifier.ps1`
requires the protected helper, exact operator SID/ACLs and operator SSH material,
validated host-operation receipts, offline gate, and a reviewed committed source
identity. Its `ReviewedSourceCommit` remains
`5ccc52b2fa35be3dde9d3839f63ec613627b796b`.

Independent host probes on 2026-10-05 found:

- Current session SID ends in `1001` (Admin), unelevated; the controller requires
  the CodexSandboxOffline SID ending in `1004`. This is an **operator-context**
  mismatch, not an observed mismatch in an installed task principal.
- `Test-Path C:\ProgramData\TBoundHostAutomation` returned false.
- `schtasks /query /tn \TBound\Inspect` returned exit 1 (task absent).
- The delegated inspection confirmed all five reviewed `\TBound\` tasks and
  protected action/policy/profile/receipt paths absent. Installer source specifies
  fixed SYSTEM/highest tasks; there is no installed principal to read back.
- Pinned-MAC rediscovery and strict pinned-key SSH succeeded to the maintenance
  guest. This existing maintenance key is **not** the controller's required
  protected operator key/enrollment and cannot bypass that policy.
- Guest `sudo -n true` returned exit 1 (password required). No refresh prompt,
  elevation attempt, install, shutdown, NIC transition or offline run was made.
- A subsequent independent controller **admission attempt** returned exit 1
  before SSH context, receipts, queueing or guest operations. It exposed a native
  Git argument construction defect at line 407: PowerShell expands
  `@('-c','safe.directory=' + $repo,'-C',$repo)` into five arguments, separating
  `safe.directory=` from the repository path. Git consequently treated the path
  as a command. Parenthesizing the concatenation and adding the mandatory
  `core.autocrlf=false` yielded a successful read-only `cat-file -e` check (exit
  0); the actual controller was not edited. This is not an offline test result.
- Delegated dummy-only argument tests also found split SSH/SCP values at lines
  230–231 (`UserKnownHostsFile=` and `HostKeyAlias=`). The current SSH vector has
  45 instead of 43 tokens; SCP inherits both defects. Neither client was invoked
  with these vectors. Native option-vector and cleanup-flow coverage is absent
  from the existing pure policy/simple-process tests.
- Static control-flow review found `allowSafeDisconnect` enabled before SSH
  context/Inspect identities exist (line 432). Cleanup ignores queue/disconnect
  stage flags and may request Disconnect after pre-queue context/SSH failure;
  task invocation precedes receipt/optional identity validation. Fixed task pins
  bound the target but do not justify disconnecting active trusted maintenance.
  This path was not executed in the failed admission attempt.

The delegated static review found the launcher/verifier hashes still match the
existing controller pins, but the initial HEAD `00ce884` does not pass its source
gate. The helper/controller's untracked/pre-existing implementation is preserved,
not silently adopted into an implementation commit.

## Host source exports (not guest evidence)

### Final verified code freeze

After independently verified slices 3 and 2, source commit is
`4cbfbf6b7beacf81de70841bf12703809b9b6bf3`. Explicit LF archive scopes
`adapter`, `supervisor`, `infra/guest`; no uncommitted overlays.
Archive SHA-256:
`816abd10797fbc34fd4107b3f99d85725a59d9c9a4943da7b1a2ab6b07654f71`.
Host source-manifest SHA-256:
`6418fe1b5092b37d0b946076bb8107fc6d5a7595b3b2b6b6a84f671d8668463e`.
All 139 exported file hashes passed readback (exit 0). Retained export is
`C:\Users\Admin\AppData\Local\Temp\opencode\tbound-frozen-4cbfbf6b7bea-d63cbdddf3ca4bed9ac01b787d9bce85`.
This freezes the tested implementation source, not current-source guest
verification: no archive transfer, queue, offline run or fresh guest report was
performed. The separately preserved controller is unchanged and remains blocked.

### Earlier export-tool check

The coordinator tested the LF export/manifest tool against the already verified
slice-1 commit `1bedc0ec350edcbb2b7e3df4d877de50f5308c66`, with no overlays,
scoped to `adapter`, `supervisor`, and `infra/guest`. Archive SHA-256:
`8c0065d22dac0717c8cc1dfc826741b08a88b6fc8b38a4aef4235edc0ba1cf9d`.
Host-only source manifest SHA-256:
`c5622e46712a2980398f6f0972cca48b8cb311566f9ebe3239a96805a0c26034`.
All 121 exported files passed manifest hash readback (exit 0). The manifest labels
guest verification blocked and guest report/artifact manifest absent. This is
neither the final five-slice source freeze nor an offline guest acceptance result;
the final verified host freeze above supersedes this export-tool check.

## Exact unlock

1. Review/approve installation of the exact hash-guarded helper bundle under its
   intended operator policy. The installer requires the pinned VM **Off with NIC
   disconnected** and a one-time authorized Admin elevation. Preserve the golden
   baseline and inspect active guest work before arranging that state.
2. Enroll the separate protected operator public key and run the controller under
   its exact intended operator SID; verify file/task/folder ACLs, task definitions,
   receipts and pinned SSH. Do not broaden the controller to the current Admin
   identity or substitute unrelated VmOps tasks/maintenance credentials.
3. Review the final verified committed code snapshot, update the reviewed source
   pin through a separately reviewed controller change, and export it with
   `git -c core.autocrlf=false archive`. Overlay no unrelated uncommitted files.
   The existing controller's Git arguments at line 407 attempt `safe.directory` but
   not `core.autocrlf=false`; its archive call at line 446 must receive that
   explicit setting in the reviewed change. A pin-only update is insufficient.
   Correct/test native argument arrays as well: line 407 must parenthesize its
   concatenated configuration value. Audit SSH/SCP arrays with dummy contexts
   before any live invocation; tested policy functions alone do not prove native
   option-vector integrity.
   Parenthesize the two dynamic SSH option values at lines 230–231 and test the
   exact Git/SSH/SCP vectors with dummy contexts. Add stubbed flow tests proving
   no cleanup task on pre-queue failures, identity-present validation **before**
   any cleanup action, and safe cleanup after uncertain queue/disconnect requests.
   Retain the intended offline safety policy without broadening operator rights.
4. Execute the reviewed offline lifecycle and verify exact source identity plus
   freshly recomputed `verification.json` and `artifact-manifest.json` hashes,
   runner/launcher exits, offline attestation and cleanup. Retain those artifacts
   before reporting a new verifier PASS.

Changing a source string alone cannot establish installation, operator access,
offline attestation or report authenticity. The controller was not changed or
installed in this blocked slice, and no repeated UAC/sudo prompting was attempted.
