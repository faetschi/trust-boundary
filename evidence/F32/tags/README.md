# F32 signed tags (register D39)

| Tag | Tag object | Commit | Content |
|---|---|---|---|
| `f32-freeze` | `025197debd00a2f2098a7f6c5c2373a0fcf9a43c` | `08f3c3ee4043171b55867dfd2dfe5258809cf40b` | the F32 build, the F-H1 candidate |
| `f32-corpus-1` | `0e11a6a78eec44f9324bd05f4723a867eb7f45ff` | `a6b52b9a055fab4d8c595ca566a2d310c1eefe97` | the first corpus-only commit on F32: `corpus/third/tmc` (TM-C DC01, DC02, DC04; PR #155) and `corpus/third/compliant` (C8; PR #93) |

Both tags were signed on 2026-11-21 with the SSH key `ssh-ed25519 SHA256:pWxRejfdFUUE5LuTqLITNEIC+BGXgG8pTHlbctNonwE`. The key was made that day only for TBound release tags, on the owner's machine, by the coordinator with the owner's go; the owner's earlier SSH key could not be unlocked. Its public half is `allowed_signers` in this folder. `main` was then fast-forwarded from `08f3c3ee` to `a6b52b9a`.

Verify (any clone):

```sh
git fetch origin tag f32-freeze tag f32-corpus-1
A=evidence/F32/tags/allowed_signers
git -c gpg.ssh.allowedSignersFile=$A verify-tag f32-freeze
git -c gpg.ssh.allowedSignersFile=$A verify-tag f32-corpus-1
git rev-parse 'f32-freeze^{commit}' 'f32-corpus-1^{commit}'
git diff --no-renames --name-only f32-freeze f32-corpus-1 | grep -v '^corpus/third/'   # must print nothing
```

Result on 2026-11-21, also from a fresh shallow fetch of the two tags: both print `Good "git" signature` for that key, the tags resolve to the commits above, and the diff has 334 paths, all under `corpus/third/`.

The main-sample cases come after the D48 main draw, in a later corpus-only commit with its own signed tag. OSF and Zenodo record the signed tags, their commits and the digest mapping (D39).
