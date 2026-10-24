# Adapter reporter samples

These outputs were captured offline in WSL with `NO_COLOR=1`, `umask 077`, a
private `TMPDIR`, and a PATH containing only the selected Node directory,
`/usr/bin`, and `/bin`. Both use the adapter test sources from
`5a29215c296814b36eacc6a955447709017c2627`, before the runner regression test.

The command in `adapter/` was:

```sh
node --experimental-strip-types --test src/*.test.ts
```

Node 22.19.0 defaults to TAP; Node 24.15.0 defaults to spec. Both samples have
37 tests, 36 passes, no failures or cancellations, and one optional Pi CLI skip.
The capture requires spec output. `scripts/test.mjs` now selects it explicitly
on every supported Node version.
