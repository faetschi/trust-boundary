# TBound guest source handoff

This procedure transfers the committed project files from the standalone repository to the Ubuntu guest, packaged beneath a top-level `tbound/` directory. It uses a tar archive and SHA-256 evidence, then SSH/SCP over the temporary provisioning connection. It does not use a host directory share or clipboard. The exporter neither contacts the guest nor installs, starts, or changes a VM.

## 1. Review and export on the host

Review `Export-TBoundGuestBundle.ps1` and this procedure first. The exporter is intentionally bound to branch `codex/tbound-prototype`; it stops on another branch, a detached HEAD, a dirty worktree, an untracked file, an unsafe source path, a non-regular tracked file, a vendored dependency directory, or a high-confidence credential pattern. Its credential scan is heuristic and non-comprehensive, so manually review committed files for secrets too. Since the handoff files are new worktree changes, review and commit them with all other intended worktree changes before running the exporter. Do not weaken the clean-worktree check to bypass that gate.

From any PowerShell working directory, run the script by its absolute path (adjust the worktree path if it has moved):

```powershell
& 'C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\trust-boundary\infra\guest\Export-TBoundGuestBundle.ps1'
```

The script writes a new commit-named folder under `F:\TBoundAssets\GuestHandoff`. It refuses an existing output folder and any reparse point in the destination path. It creates the archive from the committed repository `HEAD` tree and adds a `tbound/` prefix, so untracked and ignored checkout files, `.git`, and local dependency caches are not inputs. The committed package manifests and lock files are retained; vendored or installed dependencies are refused.

The manifest records the source branch, full commit ID, Git version, archive size, and archive SHA-256. The exporter permits only the reviewed repository-root `.gitattributes` file with the exact `*.sh text eol=lf`, `*.py text eol=lf`, and `*.go text eol=lf` rules, pinned by its committed blob hash. It rejects any other attribute file or configuration that could omit or substitute archive content. Archive creation forces `core.autocrlf=false` and a fixed tar umask, so checkout-specific autocrlf settings do not rewrite committed blob bytes. The credential scan checks a short list of high-confidence private-key, AWS, GitHub (including `github_pat_`), and credential-URL patterns; it is heuristic and cannot replace a manual review for secrets.

The exporter requires a clean worktree and rejects untracked files. If the current checkout has unrelated local files, preserve them and run the exporter from a separate clean checkout of the intended branch and committed source. It archives that checkout's committed `HEAD` only; do not weaken the dirty-worktree or untracked-file checks.

The storage gates mirror the Hyper-V scripts: the estimated bundle must keep recursive file usage in `F:\TBoundAssets` plus `F:\TBoundVMs` at or below `100GB` (PowerShell's GiB unit), and projected free space on F: must remain at least `700GB`. The exporter checks these limits at preflight, immediately before creating the archive, after staging and validating it, before writing the manifest using the actual archive size and exact manifest byte count, and after publishing it. Stage files are validated against the committed Git file list and published by a same-volume directory rename; a failed run leaves its stage in place for inspection and does not delete anything automatically. The scan refuses reparse points, junctions, and symbolic links in either tracked storage root. These are measured-file checks, not hard quotas. Power off the VM and ensure checkpoint or other writers are stopped before export; the script cannot prevent unrelated processes from writing to F: between its checks. The archive-size estimate bounds regular tar headers, per-file payload padding, and all repository directories, with additional metadata reserve.

The expected outputs are:

```text
F:\TBoundAssets\GuestHandoff\tbound-<12-hex-commit>\tbound-<12-hex-commit>.tar
F:\TBoundAssets\GuestHandoff\tbound-<12-hex-commit>\tbound-<12-hex-commit>.manifest.txt
```

Keep the printed full commit ID and SHA-256 with the transfer record. The exporter does not push the branch or write credentials to the manifest.

If the exporter reports a staging or published directory after an error, inspect it before retrying. Do not remove or overwrite the path automatically. The exporter requires Windows `tar.exe` to validate archive paths and member types before publishing.

## 2. Enable the temporary SSH transfer

Boot the guest with its provisioning NIC connected only for the transfer and provisioning phase. Use the guest console to confirm the trial account and ensure the OpenSSH server is installed and running. If it was not installed during Ubuntu setup, install it from the guest console while the temporary provisioning network is connected:

```sh
set -euo pipefail
sudo apt-get update
sudo apt-get install --no-install-recommends openssh-server
sudo systemctl enable --now ssh
```

Read the guest IP from the guest console; do not scan the LAN to discover it. In that same console, display the SSH host-key fingerprint:

```sh
set -euo pipefail
sudo ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

Create a new private incoming directory as the trial user:

```sh
set -euo pipefail
incoming="$HOME/tbound-incoming"
short_commit='<12-hex-commit>'
if [[ -e "$incoming" || -L "$incoming" ]]; then
  echo "Incoming directory already exists; inspect it before continuing." >&2
  exit 1
fi
install -d -m 700 -- "$incoming"
for candidate in "$incoming/tbound-$short_commit.tar" "$incoming/tbound-$short_commit.manifest.txt"; do
  if [[ -e "$candidate" || -L "$candidate" ]]; then
    echo "Transfer file already exists; inspect it before continuing." >&2
    exit 1
  fi
done
```

On the host, substitute the displayed guest IP, trial username, and commit suffix. Use the default SSH host-key prompt and compare it with the fingerprint shown in the guest console before accepting. If the key differs from a previously recorded key, stop and investigate; never disable host-key checking or accept an unverified key. SCP prompts for credentials interactively. Enter them only at the prompt; do not put passwords or private key material in a command, script, or log.

```powershell
$ErrorActionPreference = 'Stop'
$trialUser = 'trial-user'
$guestIp = '192.0.2.10' # replace with the address shown in the guest console
$bundleDirectory = 'F:\TBoundAssets\GuestHandoff\tbound-<12-hex-commit>'
$shortCommit = '<12-hex-commit>'
$archive = Join-Path $bundleDirectory "tbound-$shortCommit.tar"
$manifest = Join-Path $bundleDirectory "tbound-$shortCommit.manifest.txt"
& scp -o StrictHostKeyChecking=ask -o HostKeyAlgorithms=ssh-ed25519 $archive $manifest "${trialUser}@${guestIp}:/home/${trialUser}/tbound-incoming/"
if ($LASTEXITCODE -ne 0) { throw "SCP failed with exit code $LASTEXITCODE." }
```

Do not use host directory mounts, Hyper-V Guest Service Interface, or clipboard transfer. This source archive is sent only over the temporary SSH connection. If SCP fails after creating any file in the incoming directory, inspect the partial transfer and do not repeat SCP into that populated directory; use a reviewed new destination if a retry is needed.

## 3. Verify and extract in the guest

In the guest console or SSH session as the trial user, replace the commit and SHA placeholders with the host values and run this entire block in one Bash session. `set -euo pipefail` makes any failed comparison, checksum, path check, or tar type check stop before extraction:

```sh
set -euo pipefail
cd "$HOME/tbound-incoming"
archive='tbound-<12-hex-commit>.tar'
manifest='tbound-<12-hex-commit>.manifest.txt'
expected_commit='<full-commit-id-from-host>'
expected_sha256='<archive-sha256-from-host>'
if [[ ! -f "$archive" || -L "$archive" || ! -f "$manifest" || -L "$manifest" ]]; then
  echo "Expected transfer files are absent or are symbolic links." >&2
  exit 1
fi
[[ "$expected_commit" =~ ^[a-f0-9]{40,64}$ ]]
[[ "$expected_sha256" =~ ^[a-f0-9]{64}$ ]]
expected=$(awk -F= '$1 == "archive_sha256" { print $2 }' "$manifest")
source_commit=$(awk -F= '$1 == "source_commit" { print $2 }' "$manifest")
manifest_archive=$(awk -F= '$1 == "archive_file" { print $2 }' "$manifest")
[[ "$source_commit" == "$expected_commit" ]]
[[ "$manifest_archive" == "$archive" ]]
[[ "$expected" == "$expected_sha256" ]]
[[ "$expected" =~ ^[a-f0-9]{64}$ ]]
printf '%s  %s\n' "$expected" "$archive" | sha256sum -c -
tar -tf "$archive" | awk '
  /^\// || /(^|\/)\.\.($|\/)/ { bad=1 }
  $0 !~ /^tbound(\/|$)/ { bad=1 }
  END { if (NR == 0) bad=1; exit bad }
'
tar -tvf "$archive" | awk '
  { count++ }
  substr($0, 1, 1) !~ /^[-d]$/ { bad=1 }
  END { if (count == 0) bad=1; exit bad }
'
destination="$HOME/tbound-handoff-<12-hex-commit>"
if [[ -e "$destination" || -L "$destination" ]]; then
  echo "Extraction directory already exists; inspect it before continuing." >&2
  exit 1
fi
umask 077
install -d -m 700 -- "$destination"
tar --no-same-owner --no-same-permissions -xf "$archive" -C "$destination"
test -d "$destination/tbound"
```

Any failed check stops the block before extraction. The exporter only archives regular Git files; extraction happens in a new private directory owned by the trial user. If extraction itself fails, inspect the partial destination before doing anything else. The resulting setup script is at `"$destination/tbound/infra/guest/setup-ubuntu-24.04.sh"`.

## 4. Provision, prepare caches, bootstrap fixtures, then verify offline

While the temporary provisioning NIC is connected, follow the **Provisioning phase** and **Prepare pinned dependencies while online** sections of the extracted guest README. Run the reviewed setup script for the intended trial account and prepare the Go module and npm caches with the exact commands there. Keep npm lifecycle scripts disabled. Save `/var/log/tbound-guest-provision.log` and the dependency preparation record. Do not put credentials or private data in the bundle or provisioning log.

Create the fixed ownership fixtures once from the reviewed, hash-pinned bootstrap artifact during trusted connected maintenance, before a fresh guest's clean shutdown and offline checkpoint. The source `infra/guest/provision_audit_ownership_fixtures.py` takes no arguments and resolves only `tboundadmin`; it creates an empty, fixed tree at `/var/lib/tbound/audit-ownership-fixtures/<uid>` and refuses an existing UID directory. The static hash-bound bootstrap wrapper and its source hash, protected destination, and exact queue command are still pending review. It will verify the provisioner bytes, create a protected root-owned copy below `/root` without following links or overwriting existing files, then run the fixed script in isolated Python. Do not elevate a mutable checkout copy. The eventual manual invocation is `sudo /usr/bin/python3 -I <root-owned-verified-copy-of-provision_audit_ownership_fixtures.py>` with no script arguments; do not execute the placeholder before the exact pinned artifact is published. After source review, commit, and guest staging, the human privately enters `sudo -v` in the same attached `tmux` pane while the guest remains connected for maintenance; the assistant then queues only the reviewed static bootstrap. No general `NOPASSWD` rule is allowed. Keep the fixture tree quiescent while it is provisioned and tested.

A restore from a checkpoint predating these fixtures needs the one-time bootstrap again. The provisioner stops on an existing UID directory and leaves it for inspection; do not delete or recreate it. This root bootstrap flow is not yet installed or guest-verified.

The direct-console path is for a guest booted with every adapter disconnected: follow the host [Hyper-V setup runbook, Phase 2](../hyperv/SETUP.md#phase-2-install-provision-temporarily-then-disconnect), then run `python3 "$TBOUND_TREE/infra/guest/verify-offline.py" --host-adapter-disconnected` at VMConnect. SSH cannot be used after the adapter is disconnected.

For the SSH path, keep the guest running in trusted maintenance mode with its adapter connected. In the attached `tmux` pane, run the queued launcher as the non-root trial account without `sudo`. Before disconnecting, wait for `queued; waiting...`; the queueing operator must report the exact new report-directory path for this invocation. Verify that path's `status` file contains `WAITING_FOR_OFFLINE`. Do not select an older report directory by wildcard. Then disconnect the host adapter. The launcher waits for its offline gate and runs the verifier only after all non-loopback interfaces report carrier `0` and both route tables have no default route. Keep the adapter disconnected until completion. Do not first boot offline and then try to queue over SSH.

The verifier first checks the fixed fixture metadata, then runs `TestOpenRejectsUntrustedOwnership` once as the trial user; it must record one pass and zero skips. The full normal and race suites run as the trial user with the fixture-test environment variable absent and must each report only the expected skip for that test. Preserve the runner's JSON stdout as evidence. Run `probe-capabilities.sh` separately as the non-root trial user after verification.

The intended guest workflow remains pending installation and a complete offline verification result. Component test success would not establish runtime containment, a validated restore, model/provider integration, or the WP1/G1 durability gate. Linux host setup is also not yet verified.

The exporter and these instructions do not perform any transfer, guest, or VM actions automatically.
