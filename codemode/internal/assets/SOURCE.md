# Fixed QuickJS reactor

quickjs-wasi 3.6.2 source: https://github.com/vercel-labs/quickjs-wasi/tree/5a7a0eeda87c99542f8cf3095b6d61ecfa755977

The unmodified npm 3.6.2 `quickjs.wasm` is 637405 bytes. SHA256:
`d4c9375f2b1ca4dc95f72c8aa2982a7a9951ac8011490d79c6582df732b4bbd9`.

Upstream bridge source is retained as `interface.c.txt` (not compiled by Go),
SHA256 `68c6ed946041cab734fbd3d7ce68c6ba5cd79b944729229ef2237227fd333e7a`.
Its ownership rules distinguish borrowed callback values from transferred
return handles. The matching Makefile links a 1 MiB native stack. Upstream
`src/index.ts` bounds QuickJS stack usage at 512 KiB, reserving half the linker
stack for native frames. pi-go requires a positive limit <=512 KiB.

quickjs-ng submodule commit:
https://github.com/quickjs-ng/quickjs/tree/6d46d07d04041b40f4f49eaa7fdebe44c314c699

Both upstream MIT notices are retained in LICENSE.quickjs-wasi and
LICENSE.quickjs-ng. No runtime downloads or floating artifacts are used.
The source and notices are byte-for-byte upstream copies. Narrow whitespace
attributes preserve the upstream license's space-only line and the source's
final blank line; the recorded SHA checks still verify their exact bytes.

Maintenance verified 2026-10-02 via primary GitHub APIs: quickjs-wasi latest
commit 2026-09-19; wazero latest commit 2026-09-28, v1.12.0 released
2026-05-29 and requires Go1.25. This repository uses Go1.26.2. Wazero's
v1.12.0 dependency requires x/sys v0.44.0. Windows issues #2420 (an earlier
v1.9.1 Go-WASI guest) and #2406 (filesystem symlinks) are tracked upstream;
this reactor grants no filesystem and is tested on native Windows. goja
does not offer the accepted WASM isolation; Wasmtime-Go adds CGO. The fixed
wazero/QuickJS combination keeps the host pure Go and bounds guest memory.
