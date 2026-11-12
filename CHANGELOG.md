# Changelog

Notable changes are recorded here. Go module releases use `v`-prefixed
semantic version tags. Before v1.0.0, the public API may change.

## [0.2.0] - 2026-11-12

Cooperative cancellation and enforced shared memory budgets for in-process
RAW pipelines. This minor release includes breaking pre-1.0 API changes.

### Changed

- Require `context.Context` in `Open`; extend cooperative cancellation through
  bounded reads, metadata parsing, descriptor sorting and inner pixel loops.
- Enforce race-safe shared live-buffer budgets across File, RAW, preview and
  rendering lifetimes, in addition to existing per-operation limits.
- Require explicit `Close` on File and returned RAW/preview/render handles.
  Render returns `*RenderedImage` exposing its standard `*image.NRGBA64`.
- Metadata diagnostics are borrowed read-only; use `Clone` for an explicitly
  caller-owned snapshot. See the [migration and safety contract](docs/in-process-safety.md).

### Added

- Caller-owned `Budget` with atomic limit/current/peak reservation statistics,
  inherited across Open, decoding and rendering or shared across worker pools.
- Pre-allocation reservations for combined output/scratch buffers and
  conservative parser storage, with retained-output and shared-metadata lifetimes.
- Deterministic cancellation/deadline, allocation-rejection, overflow,
  concurrent-budget and ownership-cleanup tests, plus full-resolution fixture
  cancellation checks and before/after benchmarks.
- A compilable timeout/budget pipeline example and documentation of ownership,
  concurrency, cancellation latency and memory-accounting coverage.

### Limitations

- Cancellation cannot interrupt an already blocked arbitrary `ReaderAt`,
  allocation/GC pauses or downstream encoders. Reservations remain live while
  a read is blocked; no read-abandoning helper goroutines are introduced.
- Budgets bound accounted buffers, not exact Go heap/RSS, caller-owned copies
  or all dependency allocations. They do not replace subprocess isolation
  against fatal runtime failures or all memory exhaustion.
- Close invalidates result data and aliases and must not overlap their use.
  Camera/layout scope and the unmet historical certification gate are unchanged.

## [0.1.0] - 2026-11-12

Initial evidence-scoped release of the pure-Go Olympus ORF library and tools.

### Added

- Bounded ORF/TIFF and Olympus MakerNote parsing, metadata snapshots,
  per-operation capabilities and original embedded JPEG extraction.
- Exact 12-bit sensor decoding for evidenced `olympus-word16le-high12`
  and `olympus-legacy-predictive12` layouts, returning caller-owned,
  unprocessed full-sensor samples.
- Olympus OM-D E-M5 / S0036 support for the observed 4640x3472 RGGB legacy
  predictive layout and 26 normal, sequence and bracket mode tuples,
  including four-value white-balance metadata and scoped calibration.
- Independent calibrated Bayer rendering to opaque 16-bit sRGB NRGBA64,
  with black/white normalization, white balance, bilinear demosaicing,
  color conversion, exposure, active crop and EXIF orientation.
- `orfinfo` for deterministic metadata/capabilities JSON and `orfexport`
  for original previews, headerless u16le RAW and 16-bit PNG output.
  Exports publish complete new files without overwriting existing outputs.
- Explicit resource limits, typed errors, cancellation checks, independent
  result ownership and concurrent operations on immutable, safe inputs.
- Synthetic unit and fuzz tests, licensed public RAW reference fixtures,
  frozen rendering regressions, integration/race checks and portable CI.
- Development-only pinned reference tooling and an explicit private E-M5
  corpus validation workflow; private photographs are not distributed.
- API examples, rendering and layout contracts, provenance and third-party
  licensing documentation.

### Fixed

- Cross-architecture rendered-regression checks honor the documented
  calibration profile-stage tolerance.

### Requirements and limitations

- Requires Go 1.26 or newer. The production module uses only the standard
  library and works with `CGO_ENABLED=0`; native reference tools are
  development-only.
- Licensed LGPL-2.1-only, with attributed LibRaw-derived implementation;
  this is not represented as a clean-room implementation.
- Active development targets only the evidenced E-M5/S0036 files.
  Existing E-1, E-400, E-410 and E-510 paths remain historical compatibility
  behavior, not camera-wide support or certification.
- Unknown identities, modes, geometries and codecs remain unsupported.
  Newer 12/14-bit codecs, ORI, high-resolution/stacked images, non-Bayer
  sensors and non-ORF formats are outside this release's scope.
- Rendering requires evidenced calibration/crop or explicit overrides.
  E-1/E-400 profile white levels are estimates and require explicit consent.
  Rendered PNGs are not claimed to match camera JPEGs or LibRaw postprocessing.
- No camera-wide certification is claimed. Historical four-camera M1+M2
  certification requirements remain unmet; this initial release does not
  waive them or turn duplicate private files into independent captures.

See the [E-M5 contract](docs/em5.md), [rendering contract](docs/rendering.md)
and [historical readiness report](docs/release-readiness.md) for exact scope,
recorded validation and remaining limitations.

[0.2.0]: https://github.com/otuschhoff/libraw-go/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/otuschhoff/libraw-go/releases/tag/v0.1.0
