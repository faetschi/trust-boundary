# F33 signed tags (register D39)

| Tag | Tag object | Commit | Tree | Content |
|---|---|---|---|---|
| `f33-freeze` | `d4c2f240c0497c736374bee3b809ff39091e482e` | `4e8ac37d8f2cbb5a67a8a5c8b965c8345ece4e36` | `07c56d6e077018a1e1ab973b683167ca8d1811dd` | the F33 build, the F-H1 candidate: FREEZE_COMMIT, the merge of PR #288 (dev `9206c2e1` into main `a6b52b9a`), tree equal to dev's, which the full race gate g33b passed (7879 pass, 52 skip, 0 fail) |
| `f33-corpus-1` | `b8c1d77ac09214a7b3b73a63373830d2165ae104` | `2fe2c0db86133a421bb18307659cc22962a6c5b6` | `f0ba1e944cfebbfcf998a0ef4db9cbc77cecb3a9` | the corpus-only commit on F33: `corpus/third` restored byte for byte from `f32-corpus-1` (subtree `213b85f597ce14b96d3300c7be14087984deeead`): `corpus/third/tmc` (TM-C DC01, DC02, DC04) and `corpus/third/compliant` (C8) |

Main held F32's corpus-only commit (`f32-corpus-1`, `a6b52b9a`), so a plain dev→main merge would have put `corpus/third` into the F33 build commit, which D39 forbids. The owner confirmed the sequence on 2026-11-24: the merge of PR #288 drops `corpus/third` (334 files), so the build tree equals the gated tree, and the corpus-only commit restores it. Main was without `corpus/third` from 12:06:26 UTC (PR #288 merged) to 12:06:46 UTC (fast-forward to `2fe2c0db`). Nothing else merged into dev or main in between.

Both tags were signed on 2026-11-24 with the TBound release-tag key `ssh-ed25519 SHA256:pWxRejfdFUUE5LuTqLITNEIC+BGXgG8pTHlbctNonwE`, the key of `evidence/F32/tags/`. The tagger is the owner's GitHub no-reply address; its public key line is `allowed_signers` in this folder.

Verify (any clone):

```sh
git fetch origin tag f33-freeze tag f33-corpus-1
A=evidence/F33/tags/allowed_signers
git -c gpg.ssh.allowedSignersFile=$A verify-tag f33-freeze
git -c gpg.ssh.allowedSignersFile=$A verify-tag f33-corpus-1
git rev-parse 'f33-freeze^{commit}' 'f33-freeze^{tree}' 'f33-corpus-1^{commit}' 'f33-corpus-1:corpus/third'
git diff --no-renames --name-only f33-freeze f33-corpus-1 | grep -v '^corpus/third/'   # must print nothing
```

Result on 2026-11-24, also from a fresh shallow fetch of the two tags: both print `Good "git" signature` for that key, the tags resolve to the commits and trees above, `f33-corpus-1:corpus/third` is `213b85f5…`, and the diff has 334 paths, all under `corpus/third/`.
