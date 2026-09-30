# TBound guest source handoff

This procedure transfers the committed project files from the standalone repository to the Ubuntu guest, packaged beneath a top-level `tbound/` directory. It uses a tar archive and SHA-256 evidence, then SSH/SCP over the temporary provisioning connection. It does not use a host directory share or clipboard. The exporter neither contacts the guest nor installs, starts, or changes a VM.

## 1. Review and export on the host

Review `Export-TBoundGuestBundle.ps1` and this procedure first. The exporter is intentionally bound to branch `codex/tbound-prototype`; it stops on another branch, a detached HEAD, a dirty worktree, an untracked file, an unsafe source path, a non-regular tracked file, a vendored dependency directory, or a high-confidence credential pattern. Its credential scan is heuristic and non-comprehensive, so manually review committed files for secrets too. Since the handoff files are new worktree changes, review and commit them with all other intended worktree changes before running the exporter. Do not weaken the clean-worktree check to bypass that gate.

From any PowerShell working directory, run the script by its absolute path (adjust the worktree path if it has moved):

```powershell
& 'C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\trust-boundary\infra\guest\Export-TBoundGuestBundle.ps1'
```

The script writes a new commit-named folder under `F:\TBoundAssets\GuestHandoff`. It refuses an existing output folder and any reparse point in the destination path. It creates the archive from the committed repository `HEAD` tree and adds a `tbound/` prefix, so untracked and ignored checkout files, `.git`, and local dependency caches are not inputs. The committed package manifests and lock files are retained; vendored or installed dependencies are refused.

The manifest records the source branch, full commit ID, Git version, archive size, and archive SHA-256. The exporter permits only the reviewed repository-root `.gitattributes` file with the exact `*.sh text eol=lf` and `*.py text eol=lf` rules, pinned by its committed blob hash. It rejects any other attribute file or configuration that could omit or substitute archive content. A repeat export of the same commit creates the same tar content with the same Git version and archive settings. The credential scan checks a short list of high-confidence private-key, AWS, GitHub (including `github_pat_`), and credential-URL patterns; it is heuristic and cannot replace a manual review for secrets.

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

## 4. Provision, prepare caches, then verify offline

While the temporary provisioning NIC is connected, follow the **Provisioning phase** and **Prepare pinned dependencies while online** sections of `tbound/infra/guest/README.md` from the extracted tree. Run the setup script for the intended trial account, then follow the README's exact Go module download and npm cache preparation commands with `TBOUND_TREE` set to the extracted `tbound` directory. Keep npm lifecycle scripts disabled as specified there. Save `/var/log/tbound-guest-provision.log` and the dependency preparation record with the snapshot evidence. Do not add credentials or private data to the bundle or provisioning log.

After cache preparation, use the guest README's [Baseline checkpoint and offline boot](README.md#baseline-checkpoint-and-offline-boot) section together with the host [Hyper-V setup runbook, Phase 2](../hyperv/SETUP.md#phase-2-install-provision-temporarily-then-disconnect), following their prescribed order for clean shutdown, disconnected adapter, ISO ejection, disk-first verification, checkpoint, and disconnected boot. Once booted offline, follow the README's **Offline code verification** section, including its `sudo -v` step and `verify-offline.py --host-adapter-disconnected` command; preserve the runner's JSON stdout as evidence. Then follow **Offline capability probe** and run `probe-capabilities.sh` as the non-root trial user. Keep the VM offline between runs.

The exporter and these instructions do not perform any of these transfer, guest, or VM actions automatically.
