# F34 signed tags (register D39, D62)

| Tag | Tag object | Commit | Tree | Content |
|---|---|---|---|---|
| `f34-freeze` | `024857152eec2ed613604906ddac6548869e3f8d` | `8627adeb4ab3fb01728136d8b4995bf8d7c07b41` | `76a68d748e946834343850e4482260112d3f8733` | The F34 build, the final freeze (F-H1, D62). It is FREEZE_COMMIT, the merge of PR #297 (`defc3367`, built from dev `386cf312`, into main `2fe2c0db`). Its tree equals dev `386cf312`'s, which the full race gate g34c passed (7884 pass, 52 skip, 0 fail). |
| `f34-corpus-1` | `aa08f1ea3d7b4562f5c012e0231706dca752f45b` | `5e2f6cc1720f9785496f239e3e3cb8d6fa069e1f` | `de2db6f42855e78cc8e1a135aaf2b91b57a09e1b` | The corpus-only commit on F34. It restores `corpus/third` byte for byte from `f33-corpus-1` (subtree `213b85f597ce14b96d3300c7be14087984deeead`): `corpus/third/tmc` (TM-C DC01, DC02, DC04) and `corpus/third/compliant` (C8). |

F34 replaces F33 (D62). F33's tags `f33-freeze` and `f33-corpus-1` (`evidence/F33/tags/`) stay published and were neither moved nor reused.

Main held F33's corpus-only commit (`f33-corpus-1`, `2fe2c0db`). A plain dev→main merge would therefore have put `corpus/third` into the F34 build commit, which D39 forbids. The merge `defc3367` drops `corpus/third` (334 files), so the build tree equals the gated tree, and the corpus-only commit restores it. Main was without `corpus/third` from 20:33:47 UTC (PR #297 merged) to 20:34:12 UTC (fast-forward to `5e2f6cc1`). Nothing else merged into main in between.

Dev moved after the gate. #296 (brand/ink-stamp: README, `assets/`, `extensions/webchat/docs/web-chat.md` and the webchat favicon; no TBound runtime code) was merged into dev at 20:32:20 UTC. It is not part of F34. PR #297's branch points at `defc3367`, which was built from `386cf312` and not from the dev head.

Both tags were signed on 2026-11-24 with the TBound release-tag key `ssh-ed25519 SHA256:pWxRejfdFUUE5LuTqLITNEIC+BGXgG8pTHlbctNonwE`, the key of `evidence/F32/tags/` and `evidence/F33/tags/`. The tagger is the owner's GitHub no-reply address. Its public key line is `allowed_signers` in this folder.

Verify (any clone):

```sh
git fetch origin tag f34-freeze tag f34-corpus-1
A=evidence/F34/tags/allowed_signers
git -c gpg.ssh.allowedSignersFile=$A verify-tag f34-freeze
git -c gpg.ssh.allowedSignersFile=$A verify-tag f34-corpus-1
git rev-parse 'f34-freeze^{commit}' 'f34-freeze^{tree}' 'f34-corpus-1^{commit}' 'f34-corpus-1:corpus/third'
git diff --no-renames --name-only f34-freeze f34-corpus-1 | grep -v '^corpus/third/'   # must print nothing
```

Result on 2026-11-24, from a fresh clone and tag fetch:
- both tags print `Good "git" signature` for that key;
- the tags resolve to the commits and trees above;
- `f34-corpus-1:corpus/third` is `213b85f5…`;
- `f34-freeze` has no `corpus/` entry named `third`;
- the diff has 334 paths, all under `corpus/third/`.
