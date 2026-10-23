# TBound — current state

Updated 2026-10-23. Freeze **F9 = af6f38f** is installed on the guest (172.24.152.73). Engineering route OpenCode Go.

**Headline: G1 passed.** The first real governed run on the guest read a file, edited it, ran a bash command in the command cell, read the result and published the change to the live root (request `g1-req-20261023T165907Z`, `success: true`, disposition `PUBLISHED`, publication `SUCCEEDED`, transaction `pub-8e4650488051c5672504f1e50c11d682`). The live root now holds the edited `src/release.txt` and the bash-written `out/release.sha256`.

How each freeze got one stage further on the real, unprivileged, rootless guest:

| Freeze | Where the run stopped | Fixed in the next freeze |
| --- | --- | --- |
| F4 | executable "not root-owned" inside the cell | ownership via the cell's uid_map |
| F5 | read-only check never matched the root mount | `fstatfs` read-only check on the opened file |
| F6 | post-command namespace scan denied on root's processes | scan skips other users' processes |
| F7 | Bash settlement evidence compared host and cell mount IDs | bind the command view by device/inode |
| F8 | publication refused: the session binding never attested quiescence | binding carries the quiescence attestation |
| F9 | **passed** | |

F9 also logs the cause of any unknown service outcome to the journal, so a failure names itself.

| Milestone | State |
| --- | --- |
| System | Built; G1 passed on the real guest. |
| Measurement | Observers built; 18 of 22 EVQ met offline; the last 4 come from the guest steps below. |
| Research enablement | Built. Denial hints (static table, thesis treatment) and the secondary arm C-DH (decision-model hints) are in progress for the pilot freeze. |
| Route probe, direct-runtime probe, guest EVQ | Next (F9 kit steps 31, 32, 40). |
| Pre-registration, pilot, H1 | After the probes and EVQ. |

Needs you: pilot corpus review with your advisor; pre-registration decisions; OpenRouter balance for the C-DH chooser experiments when they are ready.

Next: route probe → direct-runtime probe → guest EVQ → pilot freeze (hints, C-DH, pilot budget fix, advisor-reviewed manifest) → pre-registration → pilot → H1.
