# D14 reserved capacity design

## Proposed mechanism — DRAFT, unimplemented

Candidate: one crash-persistent capacity slot in `ProtectedTrustedRoot`, shared by all
claim-bearing workflows. The slot is bound to the exact workflow, effect ID,
owner checkpoint digest, reserve-file identity, and storage identity. A second
effect is refused until the first effect's terminal audit frame, owner
checkpoint, and protected-root head all verify. This extends the production
service's one-task-at-a-time admission across workflows and restarts.

The initially considered design of calling `fallocate(FALLOC_FL_KEEP_SIZE)` on
the audit, owner, and root SQLite files is **not sound as currently specified**.
SQLite can reset, truncate, or remove/recreate a WAL during checkpoint, close,
or restart recovery. The project has no VFS hook or verified connection policy
that proves those target-file extents survive every such transition. A
successful `fallocate` followed by `stat`/`statfs` therefore does not establish
the post-effect guarantee.

The smallest candidate to validate is a service-owned, physically allocated
reserve file on a dedicated non-COW ext4 volume, plus the single root-protected
slot. Before dispatch, allocate the full terminalization budget in that file
and commit the slot. Keep the file allocated through the effect. After the
effect (or during restart recovery of an unresolved slot), the sole volume
writer truncates and syncs the reserve file, then settles the audit outcome,
owner terminal transaction, and root anchor from the newly available blocks.
The slot records the reserve inode/device/mount identity and release state; a
crash before truncation leaves the reserve intact, while a crash after it is
recognized on recovery from the file state and the still-active slot. The
volume must have an enforced single-writer/recovery gate from dispatch through
slot release; otherwise another writer could consume the released blocks and
the candidate is invalid. Production effect callbacks must not write the
protected volume: workspace/output writes belong on a separate mount. The
reserve remains allocated while an effect callback runs. Only the test's
explicit trusted capacity-fault injector writes the protected mount to real
`ENOSPC`; that injector models a storage fault and is not a product actor.

This is a candidate protocol, not yet a deployable guarantee. Truncating the
reserve under actual `ENOSPC`, ext4 journal metadata needs, restart recovery
after each protocol boundary, and the single-writer boundary must be proven by
tests and deployment enforcement. If these cannot be established, use
fixed-size authenticated emergency audit/root slots with a bounded SQLite
reconciliation path instead; do not claim D14 from per-file preallocation
alone.

Store the active slot in the protected-root SQLite database. Slot acquisition
happens before dispatch, and failure to allocate the reserve or persist the
slot denies the callback. The root terminal transaction itself is included in
the reserve budget; it is not made safe by the slot that describes it. Only the
service-owned allocator may acquire, release, and verify the reserve. Missing
capacity, changed identity, or unsupported filesystem semantics denies
dispatch.

## Integration points

- `workflowrecovery.Ledger.ReserveEffect` and
  `SQLiteOwner.CompareAndSwapCheckpoint` acquire the global slot and physically
  allocate its reserve before the effect can be authorized. The owner
  reservation and protected-root anchor remain the pre-effect gate.
- `SessionOperationAuthority` binds the exact audit journal to that ticket.
  `audit.Journal.RunEffect` writes the intent before callback admission; if that
  append fails, it never invokes the callback. The global reserve budget covers
  the maximum post-effect outcome. Every other production `RunEffect` path gets
  the same admission, including direct `sessionrepo` operations; no callback
  may bypass the ticket.
- After the effect, release/sync the reserve before terminalization. The
  resulting budget covers the audit outcome, owner main/WAL growth, protected
  root main/WAL growth, and ext4 allocation/journal overhead. Clear the global
  slot only in the terminal anchor transaction after the owner terminal head
  is confirmed.
- Recovery verifies the ticket, journal identity/range, owner event, and root
  head in a fresh process. It retains the slot for an unresolved effect and
  never interprets a missing terminal record as proof that no effect occurred.

## Bound and outstanding derivation

The enforced record caps give a useful lower bound, but not yet a safe final
reserve constant. Require and verify SQLite page size 4096 before using this
calculation. Each 16 MiB BLOB needs at most
`ceil(16,777,216 / (4,096 - 4)) = 4,101` overflow pages. A terminal owner
transaction can contain three such payload copies: checkpoint JSON in the
state row, checkpoint JSON in the event row, and proof JSON in the event row.
That is 12,303 payload pages. One main-database copy is 50,393,088 bytes;
12,303 WAL frames (4,096-byte page plus 24-byte frame header) are 50,688,360
bytes before filesystem-block rounding, or 50,692,096 bytes rounded up to
4 KiB blocks. One maximum post-effect audit outcome frame requires 8,388,608
bytes. One root page plus one WAL frame rounds independently to 12,288 bytes.
Rounding the owner database, owner WAL, audit frame, root database, and root
WAL individually to 4 KiB blocks gives a derived floor of **109,486,080
bytes** for those terminal writes. If
the pool also covers the pre-effect intent, the audit contribution doubles and
the floor is **117,874,688 bytes**. Both figures exclude SQLite B-tree/index
splits, freelist pages, the protected-root ticket update, ext4 allocation and
journal work, and allocation rounding beyond the illustrative 4 KiB. They are
floors, not deployable pool sizes.

The current code does not verify `PRAGMA page_size`, cap SQLite page counts,
or bound B-tree depth/freelist behavior. Therefore source inspection alone
cannot justify the final byte count. Before shipping, set an explicit page-size
and maximum-page policy, then derive the additional touched-page bound from
the actual owner/root schemas and validate it on the declared filesystem. The
owner schema has a state table and an event table with two event indexes; the
root terminal path updates its workflow head and capacity slot. Measure the
maximum WAL frames and simultaneous main-file growth for those exact writes at
the serialized caps, including auto-checkpoint and restart recovery. Measure
the reserve-file truncation and ext4 journal/allocation metadata at real
`ENOSPC`. Do not substitute a guessed round number or `statfs` check.

## Deployment and proof

The claim-bearing service needs a dedicated service-owned ext4 volume with a
physically allocated reserve file and no overlay, reflink, snapshot, or other
copy-on-write layer. At present the service paths are separate directories under
`/var/lib/tbound` (`audit`, `private-admin`, and `sessions`) and systemd's
`StateDirectory` does not establish a reserved volume or quota; the current
guest recipe describes a plain ext4 root partition. Provisioning must mount the
qualified volume for those protected paths, keep workspace/output storage out
of its protected allocation budget, and register the device/mount identity.
The volume must be mounted and exposed so no other service can write it from
dispatch until terminal recovery releases the slot. At startup verify ext4,
the registered identity, WAL/FULL settings, reserve-file allocation, and file
ownership. Refuse the claim-bearing service if any check fails. A dynamically
growing or differencing VHDX also needs a proven host backing-space guarantee;
guest ext4 allocation alone does not cover a host-side copy-on-write allocation
failure.

The decisive test runs the production owner/root/audit path on the qualified
ext4 volume. It acquires the slot/reserve, executes an effect callback that
writes and syncs one marker, then fills all *unreserved* blocks until the kernel
returns real `ENOSPC` before the callback returns. Reserve truncation and sync
must work in that state. The real audit outcome append, owner terminal
WAL/transaction, and protected-root CAS must then succeed from the released
reserve. Inject crashes before and after reserve release and after each
terminal write. A fresh process then verifies the marker is present once, the
exact audit outcome exists, the owner terminal checkpoint/event matches it,
the root anchors that digest, and the capacity slot is released. The test must
also prove exhaustion rejects before the callback when slot acquisition itself
cannot allocate the required reserve.

The current production code has no global capacity ticket or allocator. An
experiment using the existing per-workflow effect reservation can verify
terminal writes under a released physical reserve, but cannot claim global
cross-workflow exclusion, crash-persistent capacity-ticket recovery, or D14
completion. Those require the production slot/allocator and a follow-up test.

## Linux experiment receipt — partial evidence, not D14 completion

An earlier focused working-tree run of the Linux test at
[`reserved_capacity_linux_test.go`](../../supervisor/internal/workflowrecovery/reserved_capacity_linux_test.go)
passed on 2026-10-14 under WSL2 Ubuntu as root. It created and removed a fully
written 512 MiB loop image in a private `/root` task directory, mounted ext4
with 4 KiB blocks and `rw,noatime,nodiratime`, and verified the loop device was
detached before deleting the image. The final source-isolated run is recorded
in [`reserved-capacity-ext4-experiment-20261014.json`](../evidence/reserved-capacity-ext4-experiment-20261014.json).
It used a private archive of the recorded commit with only this test file
overlaid, captures the source and standalone test-binary hashes plus raw stdout
hash, and returned exit code 0. The earlier working-tree run receipt was
repository HEAD
`2effea581da114c0ee49caf995cf9f09ab6624c1`, test-source SHA-256
`a541415039923fc21498dbd190ef36bf57e9803eed4cfc6ad4d2505590cde0fa`, Go
`go1.27.1`, ext4 UUID `d8ba825a-b715-4654-bea2-0e8ac6d0a3b5`, mount ID `324`,
and loop device `/dev/loop0`. Its raw stdout was not retained. The isolated
binary run in the JSON used ext4 UUID `14fe4c44-9f7e-4695-8601-8f53dc06241d`,
mount ID `200`, and `/dev/loop0`; its raw log is retained outside the repository
and hashed in the receipt. Both runs identify guest ext4 behavior; neither
qualifies WSL's host backing store or establishes a no-COW host-space guarantee.

The test used a physically allocated 67,108,864-byte reserve file and held an
exclusive test `flock`. After the synced marker write and durable workflow
reservation, the test-only filler received kernel `ENOSPC` from `write`; ext4
reported `f_bavail=0` and `f_bfree=2621`. `fsync` and `syncfs` after that write
returned success, so the receipt records the actual write boundary rather than
claiming a sync error. Truncating and syncing the reserve changed its measured
physical allocation from `st_blocks * 512 = 67,108,864` bytes to zero. The real
audit `RunEffect` then appended an 8,388,606-byte outcome frame against the
8,388,608-byte frame limit; its base64 payload was 6,291,255 bytes. The real
owner terminal transaction and protected-root CAS also succeeded after the
release. A fresh process verified the exact marker, audit outcome, owner
terminal checkpoint, and root anchor.

For that small synthetic test checkpoint, the owner database had 4,096-byte
pages, 19 total pages, a 3,284-byte terminal checkpoint JSON value, an
8,335-byte terminal proof JSON value, and a 193,672-byte WAL. The protected
root had 4,096-byte pages, 6 total pages, and a 210,152-byte WAL. These are
observed totals after the test's earlier setup transactions, not isolated
terminal-transaction deltas or worst-case geometry. The test therefore does
not establish that 64 MiB covers the enforced 16 MiB owner checkpoint/proof
limits; 64 MiB is only the experiment's reserve size.

A second child exited immediately after reserve truncate and sync, before the
audit outcome, owner terminal transaction, or root terminal anchor. A fresh
process observed the zero-block reserve and the still-root-anchored
per-workflow effect reservation plus one unresolved audit intent. The next
effect in that same workflow returned `ErrPendingUnknown`, and the audit
journal denied another effect. This proves only the current per-workflow
fail-closed behavior across this crash point; no global capacity ticket exists,
so it says nothing about a new workflow bypassing the unresolved capacity
obligation.

The experiment's lock and filler are test-owned. It does not enforce a
product-wide writer/recovery gate, authenticate a global ticket, exercise
maximum-size owner/proof values, qualify the deployment volume or host backing,
or derive a final reserve constant. It is feasibility evidence for the real
guest-filesystem `ENOSPC` boundary and current terminalization code only.
The focused experiment is explicitly opt-in so broad Linux test runs skip it
before creating or mounting the 512 MiB image. A root invocation without the
opt-in variable returned a skip before creating any task directory or mount;
the output is hashed in the JSON receipt. The focused command was:

```sh
cd /mnt/c/Users/Admin/Desktop/FH/Master\ Software\ Engineering/MA\ Thesis/trust-boundary/supervisor
export TMPDIR=/tmp/tbound-d14-capacity-go GOMODCACHE=/home/jeli2k/go/pkg/mod
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly
export TBOUND_TEST_EXT4_CAPACITY=1
/home/jeli2k/go-sdk/go/bin/go test ./internal/workflowrecovery -run '^TestReservedCapacityFilesystemExperiment$' -v -count=1
```
