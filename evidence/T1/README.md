# T1: the pilot draw commitment (D48, D53)

The commitment for the random pilot draw is public as Zenodo record [24448833](https://zenodo.org/records/24448833), DOI `10.5281/zenodo.24448833`, titled "TBound H1 evaluation: commitment for the random pilot draw (T1)".

| Fact | Value | Source |
|---|---|---|
| T1 | `2026-11-22T20:40:05Z`: the record's `created` time, `2026-11-22T20:40:04.679869+00:00`, rounded up to the next whole second (D53) | `record.txt` |
| Seed round R | `6667847`, scheduled `2026-11-23T20:40:30Z`: the first round of the committed drand chain at least 24 h after T1 | `record.txt` |
| File | `pilot-commitment-v1.json`, SHA-256 `64cecacc5d3eced9968ff15e94aac69784a14f191818cda9126f3a0f5b598fa5`, MD5 `f0683f9877ac60ac221bfe494b697aa3` (the record's checksum) | this folder |
| Signed tag | `h1-sampling-frame`, tag object `884a6591c561dd7eb5bf119bdd3efb73f13a9e8f`, commit `08d357ad5d78708b358f1ab6da05b3ccc5b4514f` (the merge of PR #263) | `h1-sampling-frame.tag` |
| Signing key | `ssh-ed25519 SHA256:pWxRejfdFUUE5LuTqLITNEIC+BGXgG8pTHlbctNonwE`, the F32 tag key | `evidence/F32/tags/allowed_signers` |

The 7 files the commitment binds by digest are byte-identical at `08d357ad` and at `f32-freeze`.

The draft was created at `20:35:53Z` and published at `20:40:05Z`, after the owner reviewed it in the web UI (`draft.txt`, `publish.txt`). The record's `created` is the publish time, not the draft's creation time: `created` and `updated` are 0.3 s apart (`record.txt`).

`h1-sampling-frame.tag` is the raw tag object (`git cat-file tag`). `git hash-object -t tag h1-sampling-frame.tag` prints the tag object above. The tag was pushed after the publish. The record already binds its object hash, so the push time does not matter.

Verify (any clone, with network):

```sh
git fetch origin tag h1-sampling-frame
git -c gpg.ssh.allowedSignersFile=evidence/F32/tags/allowed_signers verify-tag h1-sampling-frame
git rev-parse h1-sampling-frame 'h1-sampling-frame^{commit}'      # 884a6591..., 08d357ad...
py -3 -I corpus/sampling/t1_prepare.py record --id 24448833 --file evidence/T1/pilot-commitment-v1.json   # file equals; T1 and R as above
```

Use `python3 -I` instead of `py -3 -I` on Linux. The two `FILL` fields of the template at `08d357ad` are the only bytes that differ from this file.
