# Observed browser build result

This API supports tunaOS#3470 and iso-builder#270. It is not a release
receipt, authentication claim, or qualification result. No deployed caller or
engine pin changes with this implementation.

## Compatibility boundary

Keep `tboxBuildIso(opts, onChunk)` returning its current numeric byte count.
Add `tboxBuildIsoWithResult(opts, onChunk)` through the same build implementation.
Its successful result contains `bytesWritten` and versioned `observedInputs`.
No global last-result getter is provided. Failures reject without a result;
the caller must discard partial output. Reset or a later operation cannot
reuse a preceding operation's result.

The existing asynchronous entry points share mutable image and OPFS state.
Before adding evidence, use one operation-scoped state snapshot and reject
overlapping introspect/build/reset operations. Otherwise a result can describe
another operation's image, even when every individual digest is correct.
Test concurrency, reset, rejected sinks, retry and failed introspection.

## Observations and their limits

- Source manifest: hash the exact platform-manifest bytes selected for this
  operation. Preserve the already verified index descriptor relationship.
- Index: retain the hash of the exact index bytes used for selection, when
  present. A direct manifest has no observed index. Do not reread a mutable tag
  after the build and treat that as the selected index.
- Platform: return the selected descriptor's architecture and variant as
  descriptor evidence, not measured hardware compatibility. A direct manifest
  lacks this evidence until a configuration blob is fetched and verified.
  Requested platform and CPU baseline are not substitutes for observations.
- Config: distinguish a descriptor read from the source manifest from config
  bytes consumed and digest-verified by the engine. Current offline-layout
  code consumes config bytes; ordinary builds do not. Do not promote the
  descriptor to verified content or force an extra download for bookkeeping.
- Overlay: retain the resolved overlay manifest digest at graft time. Record
  whether its layers were successfully applied and whether an actual base
  annotation was present and matched. Missing annotations are not verified
  base linkage. Custom-image and absent-overlay cases remain explicit.
- Boot inputs: a later step can hash the exact kernel, assembled initramfs and
  selected EFI payload bytes while they are consumed. Merely naming their
  paths or copying requested IDs is not byte verification.

The build request remains expected input. Never populate observed fields by
copying `resolvedInputs`, installer revision, engine revision, catalog identity
or requested target. Frontend identity requires its producer's own receipt;
the backend hash cannot stand in for the frontend commit.

## Small implementation sequence

1. Fix and regress direct-manifest expected-digest enforcement separately.
   The current OCI resolver checks pins only after index selection.
2. Add per-operation resolution observations without changing legacy resolver
   selection semantics. Two current resolvers differ on amd64/v2 fallback;
   consolidation or policy changes are a separate reviewed decision.
3. Add overlay observations at the existing apply boundary, with nil result
   on errors. Test mismatch, absent overlay, zero-layer overlay and failed apply.
4. Add the compatible WASM result API and actual JS/native tests for old/new
   return shapes, interleaving, sink errors and no stale results.
5. Only after review and native CI: pin the engine in iso-builder and consume
   the result. Join independent browser media hashes and trusted artifact
   context to the schema; retain diagnostic status until real boot/install
   acceptance and producer frontend identity are available.

## Verification scope

Use in-memory HTTP transports and tiny synthetic layers, no production image
downloads or physical disks. Test observed digests against exact fixture bytes,
not fixture labels. Run package tests, `go vet`, native and WASM builds, then
existing smoke gates on the final published head. A synthetic test proves the
API behavior, not that a real ISO boots or installs.

## Public API contract

- tboxBuildIso keeps its numeric Promise result. tboxBuildIsoWithResult returns
  an object with bytesWritten and observedInputs (schemaVersion 1).
- tboxReset keeps its successful synchronous null return and immediately
  invalidates the source. OPFS deletion completes asynchronously. A subsequent
  operation waits for that cleanup before accessing files.
- tboxResetAsync returns a Promise that completes after cleanup. It is useful
  when callers need cleanup errors or completion explicitly.
- A reset or another stateful call during an active operation rejects without
  mutating the active operation. Callers must handle that rejected Promise.
- Build cleanup errors reject the build without a result. Reset cleanup errors
  block subsequent operations. Retrying reset retries incomplete cleanup;
  successful removals are remembered.
- Failed introspection clears prior facts and evidence. Each build clones only
  tree metadata and xattrs, retaining immutable blob refs. Per-build post and
  offline-metadata arenas are discarded, so retries start from the same base.
- The source config descriptor is retained without extra I/O. bytesVerified
  becomes true only after the offline build consumes and verifies the blob.
- Overlay status absent means no overlay was applied under the existing
  optional-overlay policy; it does not establish that a registry has no overlay.

The engine does not synthesize requested CPU baseline, hardware target, frontend
revision or boot proof. The selected platform descriptor is explicitly labeled
as descriptor evidence. Synthetic runtime tests exercise contracts and isolation,
not firmware, a bootable fixture, or installation qualification.
