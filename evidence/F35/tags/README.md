# F35 signed tags (register D39, D63)

| Tag | Tag object | Commit | Tree | Content |
|---|---|---|---|---|
| `f35-freeze` | `84998b4bc237d2cce70662ab24402d33f435da13` | `9165c43657951f7b3db972cfe228b68249312a7e` | `3979713e7326e3120ad60acd4121ced9b9bc8caa` | The F35 build, the final freeze (F-H1, D63). It is FREEZE_COMMIT, the merge of PR #312 (`47ef959d`, built from dev `860c2379`, into main `5e2f6cc1`). Its tree equals dev `860c2379`'s, which the full race gate g35b passed (7912 pass, 52 skip, 0 fail). |
| `f35-corpus-1` | `bb659c3e36c04c8af4e53f82ac3b75d45a84bbe0` | `0cb3af3d194d7c62a5e79567b11dc3c53ce3117b` | `cab2c5aea83e1d93ab393fed6bd166ced96b6e4e` | The corpus-only commit on F35. It restores `corpus/third` byte for byte from `f34-corpus-1` (subtree `213b85f597ce14b96d3300c7be14087984deeead`): `corpus/third/tmc` (TM-C DC01, DC02, DC04) and `corpus/third/compliant` (C8). |

F35 replaces F34 (D63 and its amendments, thesis #70, #71, #72). F34's tags `f34-freeze` and `f34-corpus-1` (`evidence/F34/tags/`) stay published and were neither moved nor reused.

Main held F34's corpus-only commit (`f34-corpus-1`, `5e2f6cc1`). A plain dev→main merge would therefore have put `corpus/third` into the F35 build commit, which D39 forbids. The merge `47ef959d` drops `corpus/third` (334 files), so the build tree equals the gated tree, and the corpus-only commit restores it. Main was without `corpus/third` from 20:41:58 UTC (PR #312 merged) to about 20:42:17 UTC (fast-forward to `0cb3af3d`). Nothing else merged into main in between.

Dev did not move between the gate and the merge: `860c2379` is the merge of #310, whose head `7cbc9e52` the gate ran on, and its tree is the gated tree.

Both tags were signed on 2026-11-26 with the TBound release-tag key `ssh-ed25519 SHA256:pWxRejfdFUUE5LuTqLITNEIC+BGXgG8pTHlbctNonwE`, the key of `evidence/F32/tags/` to `evidence/F34/tags/`. The tagger is the owner's GitHub no-reply address. Its public key line is `allowed_signers` in this folder.

Verify (any clone):

```sh
git fetch origin tag f35-freeze tag f35-corpus-1
A=evidence/F35/tags/allowed_signers
git -c gpg.ssh.allowedSignersFile=$A verify-tag f35-freeze
git -c gpg.ssh.allowedSignersFile=$A verify-tag f35-corpus-1
git rev-parse 'f35-freeze^{commit}' 'f35-freeze^{tree}' 'f35-corpus-1^{commit}' 'f35-corpus-1:corpus/third'
git diff --no-renames --name-only f35-freeze f35-corpus-1 | grep -v '^corpus/third/'   # must print nothing
```

Result on 2026-11-26, from a fresh clone and tag fetch (20:43 UTC):
- both tags print `Good "git" signature` for that key;
- the tags resolve to the commits and trees above;
- `f35-corpus-1:corpus/third` is `213b85f5…`;
- `f35-freeze` has no `corpus/` entry named `third`;
- the diff has 334 paths, all under `corpus/third/`.
