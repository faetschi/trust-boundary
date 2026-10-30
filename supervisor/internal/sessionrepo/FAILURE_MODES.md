# Session repository failure analysis

This package owns the prototype's logical generation lifecycle. These failures
must be rejected or leave the session quarantined before a generation/result is
released:

1. The repository root or an internal directory is a symlink, has the wrong
   owner/group/mode, or aliases the source tree.
2. A caller supplies an empty, duplicate, malformed, or reused operation or
   generation identity; the store must not overwrite a prior generation.
3. A seed, source generation, or command view changes during traversal/copy;
   the observed tree must match a complete stable scan before promotion.
4. An import contains a symlink, hard link, special file, nested mount, visible
   xattr/ACL/file capability, noncanonical stored mode, or unsupported path.
5. An edit or delete target is absent, non-regular, linked, too large, or
   replaced by a link between path lookup and open; a delete may only remove a
   regular file already present in the approved tree.
6. An edit replacement is empty, occurs zero/multiple times, or overlaps
   another replacement; the operation must not partially update its source.
7. A write/edit exceeds file/tree limits, produces a malformed manifest, or
   changes a path outside the approved relative-path grammar.
8. Normalized contents, executable bits, ownership, or metadata-policy identity
   differ after import; timestamps and non-executable permission differences
   must not alter tree identity.
9. A command view aliases its sealed input, or mutation of the view changes its
   source generation.
10. Command settlement is missing, refers to another lease/view, or does not
    report the process scope empty and view detached; import must be withheld.
11. Command output changes a sealed input, contains unsupported metadata, or
    cannot be mapped completely to an approved transition.
12. An allow receipt is missing, denied, for another policy/metadata policy, or
    the trusted decision verifier rejects the exact transition binding.
13. The pre-effect journal intent cannot be synced; the callback must not run.
14. The operation succeeds but the terminal journal record cannot be synced;
    the result must be withheld and the store quarantined as unknown.
15. A staging write, file sync, directory sync, or no-replace promotion fails;
    partial output must never appear as a committed generation.
16. A generation is modified or replaced after sealing; future reads, clones,
    chain verification, and results must fail closed.
17. A previous generation is used as the input after the chain has advanced;
    forks and stale-source transitions must be rejected.
18. The ordered transition ledger has a missing object, wrong preimage,
    duplicate path/identity, broken digest link, or final manifest mismatch.
19. A read path escapes the generation, traverses a symlink, exceeds its bound,
    or changes while being read; no unverified bytes may be returned.
20. A failed operation is retried or a command view is consumed twice; effect
    identity and view state must prevent replay.
21. An artifact omits the baseline manifest, an intermediate manifest, a
    transition, or a terminal outcome; verification must fail rather than infer
    success from a final tree alone.
22. After restart, the private state cache is ahead of or conflicts with the
    verified audit prefix; recovery must reject it. A valid older cache prefix
    may be advanced only from complete successful effect results.
23. An unresolved/unknown effect, unjournaled generation, staging directory, or
    leftover writable command view is present at restart; recovery must
    quarantine rather than guess whether the command ran or its writers stopped.

This layer can establish descriptor-based logical ownership, private staging,
byte-copy independence, stable manifests, and ordered transition reconciliation.
It cannot prove protection from another process with the same UID, a read-only
Pi mount, writer termination, a detached command mount, kernel containment, or
real provider correlation. The caller must later bind those properties to the
registered runtime, process, and mount observers. A passing package E2E test is
not a WP1/G1 or E05 result.
