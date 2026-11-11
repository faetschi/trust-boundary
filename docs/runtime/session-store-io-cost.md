# Session-store I/O cost per tool call

Measured 2026-11-11. This page records how the session store's work per tool
call grows with project size, what was changed to reduce it, and what remains.
These numbers come from a developer WSL host, not the reference guest. They are
not SQ-C results and not thesis evidence.

## What each call does

Every call works on the whole project tree, not on the changed files. A walk
opens, checks and hashes every file. A copy is a walk that also writes every
file. Counts are for the current code.

| Call | Whole-tree walks | Of which copies |
| --- | --- | --- |
| Seed (session start) | 3 | 1 |
| `read` | 3 | 0 |
| `write`, `edit` | 6 | 1 |
| `bash` | 8 | 2 |

The walks are deliberate checks, not duplicates: each one verifies a sealed
generation or a fresh copy at a different point (before authorization, after
the command, after promotion). `TestRunBashScanRefusesViewChangedAfterImportAndRemovesIt`
pins one of them. This change keeps every walk and makes each one cheaper.

Every promoted generation is a full copy and is kept, so store disk use grows
with calls × project size (about 86 MB per generation for django below).

## What changed

1. **Mount ID with `statx`.** The walk checks every file's mount ID up to three
   times. It read `/proc/self/fdinfo/<fd>` each time (open, read, parse, close).
   It now uses one `statx(STATX_MNT_ID)` call on the held descriptor, which
   returns the same ID. Kernels before 5.8 lack `STATX_MNT_ID`; the walk then
   fails closed. `TestStatxMountIDMatchesFdinfo` checks both IDs agree.
2. **One `syncfs` per stage instead of one fsync per file.** The session store
   now imports with `workspace.ImportUnsynced`. A stage (a new generation) is
   made durable by one `syncfs` in `syncStageTree`, before the existing directory
   syncs and the rename. The throwaway command view gets no file sync: recovery
   refuses any leftover view, and the cell reads it through the page cache.
   `workspace.Import` keeps its per-file fsync for its other callers (private
   Git, publication restore). `syncfs` reports writeback errors from Linux 5.8,
   the same minimum as the mount-ID check. It flushes the whole filesystem,
   including what a command wrote into its view; per-file fsync wrote those
   files too, one by one. The sweep's runner writes one file, so a command that
   writes much more is not measured here.

No digest, manifest, evidence format or check changes. Fault tests: the three
`SiteWorkspaceImport/OpFsync` subcases are removed because the store no longer
calls that fsync; a data-sync failure now surfaces at `stage-tree-fsync`, which
is now also injected on the Bash path (after the admission record).

## Results

Pinned SWE-bench repositories, fetched by `scripts/size-sweep-fetch.sh`. Five
calls of each kind per repository, interleaved (read, write, bash); medians.
The test runner writes one file and starts no process, so cell start,
`podman` and the per-call capability probe are not included.

| Repository | Files | Size | Call | Before | After | Speed-up |
| --- | ---: | ---: | --- | ---: | ---: | ---: |
| flask | 236 | 1.8 MB | seed | 0.57 s | 0.18 s | 3.2× |
| | | | read | 0.08 s | 0.05 s | 1.5× |
| | | | write | 0.68 s | 0.17 s | 3.9× |
| | | | bash | 1.21 s | 0.24 s | 5.1× |
| pytest | 644 | 7.5 MB | seed | 1.92 s | 0.37 s | 5.2× |
| | | | read | 0.18 s | 0.12 s | 1.5× |
| | | | write | 1.74 s | 0.41 s | 4.3× |
| | | | bash | 3.10 s | 0.59 s | 5.2× |
| astropy | 2,043 | 29.8 MB | seed | 4.87 s | 1.28 s | 3.8× |
| | | | read | 0.60 s | 0.38 s | 1.6× |
| | | | write | 5.63 s | 1.25 s | 4.5× |
| | | | bash | 10.02 s | 1.92 s | 5.2× |
| django | 7,100 | 46.3 MB | seed | 18.93 s | 4.94 s | 3.8× |
| | | | read | 2.41 s | 1.44 s | 1.7× |
| | | | write | 20.12 s | 5.10 s | 3.9× |
| | | | bash | 38.00 s | 7.86 s | 4.8× |

Steps on django (medians, seconds):

| Build | read | write | bash |
| --- | ---: | ---: | ---: |
| Before | 2.41 | 20.12 | 38.00 |
| `statx` only | 1.48 | 18.57 | 34.96 |
| `statx` + `syncfs` | 1.44 | 5.10 | 7.86 |
| Bound: no file sync at all (throwaway build) | 1.43 | 4.70 | 6.96 |
| Also no view directory sync (throwaway build) | 1.42 | 4.98 | 7.71 |

Dropping the view's directory sync made no difference beyond run-to-run noise
(single runs; the two throwaway builds differ by about 10 % in the wrong
direction), so it stays. Two fault tests use it as their seam.

**Over the limit.** pingdotgg/t3code (31,962 files, 379 MB, 17 symlinks removed)
is refused at Seed: the walk reaches the 20,000-object limit. The refusal comes
after 42 s before and 7 s after, and it reads as an audit failure ("effect may
have occurred"), not as a clean size refusal.

**A lower practical limit.** The admission record holds both full manifests and
must fit in one 8 MiB audit record; the record data is base64 in the frame, so
the usable budget is about 6 MiB. A parallel measurement on main `13ed1193`
(uniform 2 KiB files) saw `RunBash` fail after the command had run at 19,697
objects with "audit record exceeds the configured record limit"; 9,900 objects
still passed. `write` and `edit` append the same record. So the working limit is
somewhere between those two sizes, below the 20,000-object cap. django (7,100
objects) passed every call in this sweep. A refusal before
the effect is tracked separately (workspace I/O plan, P2).

## What remains

With the file syncs gone, the CPU profile of the django run shows the walks
themselves (about 60 % of the time, mostly syscalls for the per-file checks),
directory creation (about 13 %), and the canonical-JSON manifest digests (about
13 %). `bash` time also grows slowly with the call index (7.5 s to 8.8 s over
five calls on django). Reducing the walks further means changing what is
checked or how generations are stored, which is a design change, not a speed-up.

Host: WSL2 kernel 5.15, ext4, 32 CPUs, single run per build. The reference
guest uses ext4 on a Hyper-V VM; its absolute numbers will differ.

## Rerun

```sh
scripts/size-sweep-fetch.sh "$HOME/sweep-repos"   # prints SIZE_SWEEP_SOURCES=...
cd supervisor
SIZE_SWEEP_SOURCES=... SIZE_SWEEP_CALLS=5 \
  go test ./internal/sessionrepo/ -run '^TestSizeSweepBenchmark$' -count=1 -v
```

Set `TMPDIR` to a mode-0700 directory under `$HOME`; the store refuses a
world-writable ancestor such as `/tmp`. `SIZE_SWEEP_CPU_PROFILE=<prefix>` writes
one CPU profile per repository.
