# Phase 7: usable engineering milestone, release blocked

**v0.1.0 release scope:** the initial tagged release covers the implemented,
evidence-scoped library and commands, with E-M5/S0036 as the active target.
It is not the historical four-camera certified M1+M2 release described below
and does not waive its unmet certification requirements. See the
[changelog](../CHANGELOG.md) for the release summary.

**Historical four-camera milestone:** current work is now restricted by the
owner to [E-M5/S0036](em5.md). The fixture gaps and certification command in
this report describe the earlier public regression corpus, not a requirement
to develop or acquire other camera types for the active target.

Phase 7's engineering work is implemented: runnable metadata/preview/RAW/PNG
examples, full-resolution rendered regression artifacts, filesystem/in-memory
API parity, repeatable measurements, and documented limitations and licensing.
The owner explicitly approved completing this work **without waiving** the
two-independently-captured-fixtures-per-camera/mode release requirement.

**No camera is certified and no M1+M2 release tag is authorized by this milestone.**
There is still only one approved original per initial camera/mode. Additional
photographs, their individual permissions, independent-capture review and
reference validation remain prerequisites, not optional follow-up testing.

## Evidenced scope, not camera-wide support

All rows describe 12-bit samples, one stored Bayer frame and the exact identities
and modes below. Firmware is unknown. Software tags are 1.1 for the word layout
and 1.3 for predictive inputs; software is not an invented firmware version.

| Model / camera identity | Layout / uncropped RAW | SpecialMode / DriveMode | Rendered crop | White / default rendering | Approved originals |
|---|---|---|---|---|---:|
| E-1 / D4040 | word16le-high12 / 2624x1966 | `0 0 0` / `0 0` | 2560x1920 | 4095 estimate; explicit consent required | 1 of at least 2 |
| E-400 / S0009 | word16le-high12 / 3768x2840 | `0 0 0` / `0 0 0` | 3648x2736 | 4095 estimate; explicit consent required | 1 of at least 2 |
| E-410 / S0013 | legacy-predictive12 / 3720x2800 | `2 1 0` / `1 1 0` | 3648x2736 | 3946 profile; available within limits | 1 of at least 2 |
| E-510 / S0010 | legacy-predictive12 / 3720x2800 | `0 0 0` / `0 0 0` | 3648x2736 | 3946 profile; available within limits | 1 of at least 2 |

E-410's sequence metadata does not establish support for stacked or multi-frame
payloads. Profiles and decoders remain layout/identity/mode/geometry scoped,
not selected by camera name alone. Metadata, preview, RAW and default rendering
have independent capabilities and actionable reasons for unavailability.

Deferred: newer parameterized 12/14-bit codecs, other identities or modes, ORI,
high-resolution/stacked images, multi-strip predictive restart schemes,
non-Bayer sensors and non-ORF formats. Unsupported RAW must return an explicit
error, never substitute a JPEG. Metadata/preview may remain usable independently.

## Runnable examples

Run from the repository root; the production module uses only Go's standard
library and needs neither CGO, LibRaw, ExifTool nor a subprocess.

```sh
CGO_ENABLED=0 go run ./cmd/orfinfo photo.orf
CGO_ENABLED=0 go run ./cmd/orfexport -format preview -output preview.jpg photo.orf
CGO_ENABLED=0 go run ./cmd/orfexport -format raw -output sensor.u16le photo.orf
CGO_ENABLED=0 go run ./cmd/orfexport -format png -output developed.png photo.orf
```

`orfinfo` emits metadata/capabilities JSON, including calibration origins and
diagnostics. RAW output is **headerless, tightly packed little-endian uint16**:
exactly `2*width*height` bytes, in full sensor row order. Obtain geometry, CFA,
active area, orientation and calibration from metadata. RAW has no black
subtraction, WB, scaling, crop, orientation or display-color conversion.

Preview output is the original embedded JPEG, byte for byte. Its orientation
may be unknown; do not assume the sensor's EXIF transform applies to its bytes.
PNG output is a separately developed full active crop with 16-bit sRGB-encoded
channels and opaque alpha. The standard PNG encoder can omit the redundant
opaque alpha channel; decoding still retains all 16-bit color values.
No ICC profile, original EXIF, camera tone curve or JPEG-matching claim is added.

For E-1/E-400, consciously permit the **estimated**, not measured, white level:

```sh
CGO_ENABLED=0 go run ./cmd/orfexport -format png -allow-estimated-white \
  -exposure 0.5 -output developed.png photo.orf
```

Zero exposure is the default. `-preserve-orientation` opts out of EXIF
orientation. Non-default PNG-only settings are rejected for RAW/preview export.
Flags precede the positional input; `--` permits an input name beginning with
`-`. Use `-h` for help. Exit codes: 0 success/help, 2 invalid arguments,
1 operation failure with an explicit stderr error.

Outputs must be **new files**. The exporter writes/closes a private temporary
file in the destination directory, then atomically hard-links the complete
file without replacing an existing destination, and removes its temporary
name. Successful output permissions are 0600. Encoding errors, cancellation,
existing files and unsupported hard links fail explicitly; there is no
overwrite/copy fallback. SIGINT is checked during rendering/writing and before
publication. This is atomic publication on supported Linux/macOS filesystems,
not a claim of crash-durable fsync or support for every filesystem.
PNG encoding has its own standard-library allocations, outside `Render`'s
explicit buffer budget.

## Filesystem and in-memory ReaderAt

The [public examples](../example_test.go) compile against the exported API.
For a filesystem input, open an `*os.File`, obtain its size, call
`libraw.Open(input, size, opts)` and keep it open until operations finish.
The library borrows the immutable input; `File` has no `Close` method.

An in-memory input uses exactly the same API, with no new buffering layer:

```go
func decodeMemory(ctx context.Context, data []byte) (*libraw.RawImage, error) {
    file, err := libraw.Open(bytes.NewReader(data), int64(len(data)), libraw.OpenOptions{})
    if err != nil {
        return nil, err
    }
    return file.DecodeRaw(ctx)
}
```

Keep the backing bytes immutable while the file is in use. Metadata snapshots,
preview bytes, RAW samples and rendered pixels belong to the caller; results
do not alias each other or the borrowed input. Concurrent calls require a
concurrency-safe immutable `ReaderAt`, and callers must not mutate shared
RAW/options during rendering.

Zero `OpenOptions` resolve bounded defaults, not unlimited input. Rendering
uses separate `RenderOptions.Limits`; pass the same limits explicitly if
maintaining one policy across operations. Limits apply per operation, not to
the sum of concurrent results or PNG encoding. See the [API/M1 contract](m1.md)
and [rendering contract](rendering.md) for precise quotas and defaults.

Use `errors.Is` for typed failures such as `ErrNotORF`, `ErrTruncated`,
`ErrCorrupt`, `ErrUnsupported`, `ErrLimitExceeded`,
`ErrCalibrationUnavailable`, and context cancellation. Missing calibration,
missing crop, crops smaller than 2x2, and unknown orientation do not cause
invented fallback values. Present render overrides are explicit, validated
caller choices; E-1/E-400 estimated white still requires permission.

## Frozen rendered regressions and provenance

[The render index](../testdata/render/index.json) binds four full-resolution
16-bit PNGs to original input hashes/archive IDs, unchanged independent RAW
sample hashes, complete metadata/calibration, exact render options, dimensions,
PNG lengths/hashes and decoded opaque NRGBA64 hashes. It records Go 1.26.0,
darwin/arm64, renderer/color/profile source hashes, and hashes of the independent
RAW index and individual CC0 evidence.

These are **project-renderer regression baselines, not an independent LibRaw
color/postprocessing oracle**. Independent equations and 257 sensor-coordinate
probes per image remain in addition to frozen-image comparisons. The RAW
oracle remains the unchanged pinned LibRaw 0.21.4 output.

Integration checks every RGB channel of all **34,857,984 rendered pixels**:
**104,573,952 RGB codes**, with maximum permitted absolute difference **one
uint16 code**, and alpha exactly 65535. Artifact bytes and decoded pixel hashes
are exact; another SDK/architecture need not encode identical PNG bytes.
Filesystem/in-memory parity checks metadata, capabilities, original previews,
every RAW sample/calibration field and every rendered byte, with independent
ownership and unchanged input. CLI integration verifies actual sensor-byte
hashes, original preview bytes, full 16-bit PNG pixels and estimated-white gating.

### Reproduction and intentional changes

Ordinary tests never download photographs or invoke native references.
Fixture fetching is an explicit, individually permission-checked operation:

```sh
go run tools/reference/main.go fetch
go run tools/reference/main.go verify
go run ./tools/rendergolden verify
```

Regenerate candidates only with the pinned SDK into **new** directories:

```sh
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go run ./tools/rendergolden generate .cache/review-render-a
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go run ./tools/rendergolden generate .cache/review-render-b
diff -qr .cache/review-render-a .cache/review-render-b
```

Generation refuses a wrong original hash or any RAW sample hash/count mismatch
before freezing that image. Three local freezes produced identical PNG bytes;
two initial complete candidate directories were byte-identical. Partial failed
candidates are retained explicitly for inspection, not silently published.
Review intended pixel changes, numerical-stage tests, source/calibration
provenance and independent RAW parity before promoting a candidate. Never
regenerate the independent RAW oracle from this renderer. Native reference
reproduction remains the opt-in workflow in [the reference guide](../tools/reference/README.md).

Recorded renderer hashes describe the code used to freeze the baseline; they
are not a requirement that every later behavior-preserving refactor rewrite
the PNGs. Integration compares current parsed metadata/options with the frozen
case and current pixels with the baseline. Derived profile camera-to-XYZ
coefficients use the existing `1e-6` absolute stage tolerance for architecture
roundoff; all other metadata, calibration provenance and options remain exact.
The offline verifier validates
image bytes/pixels, source bindings, licenses, coverage and index structure.

The PNG photographs remain CC0-1.0, while the metadata index, LibRaw-derived
calibration observations and project tooling retain LGPL-2.1-only terms.
Separate image/metadata license fields prevent relabeling calibration data CC0.
See [third-party notices](../THIRD_PARTY.md) and saved individual source rows.
No reference binaries or original ORFs are distributed in this milestone.

## Validation and performance

Go 1.26.0 macOS/arm64 full integration, race integration, vet, CGO-disabled
build, independent reference-tool tests and original/RAW/render integrity
checks pass. Go 1.27.3 integration and render integrity pass. All frozen RGB
comparisons on these SDKs have zero differences, below the one-code bound.
Root-package coverage remains 91.8%; exporter coverage is 86.6%.
The development freezer's explicit photo-dependent generation is validated
separately, rather than invoked by ordinary unit tests.

A read-only, network-disabled Go 1.26.0 Linux/arm64 container also passes full
integration, vet, build and both artifact workflows. Its 386 check is
**compile-only**. The [CI workflow](../.github/workflows/ci.yml) enforces native
Linux/macOS amd64/arm64 integration/races, minimum/current SDKs, actual 386
execution, production dependency/native-input checks and seven fuzz smokes.
CI status belongs to each commit; all required checks must be green before
any release, and engineering CI does not waive certification.

Seven new ten-second fuzz smokes passed **3,496,894 executions** without failure.
These are not new ten-minute gates: the Phase 5 six-target long runs and Phase 6
renderer long run remain explicitly historical evidence. No decoder or
production renderer implementation changed in Phase 7.

Unloaded Apple M1 Max, macOS/arm64, Go 1.26.0, `CGO_ENABLED=0`, three 300-ms
repeats, using the fixed hashes/dimensions printed by existing benchmarks:

| Fixture | Open | RAW decode | Render | Render MP/s | Render B/op | Allocations |
|---|---:|---:|---:|---:|---:|---:|
| E-1 | 62.1-63.6 us | 8.00-8.08 ms | 787.6-860.8 ms | 5.71-6.24 | 39,387,488 | 4 |
| E-400 | 70.1-70.6 us | 15.83-16.43 ms | 1561.9-1624.5 ms | 6.14-6.39 | 79,937,888 | 4 |
| E-410 | 54.8-56.6 us | 393.09-393.68 ms | 1581.1-1624.4 ms | 6.14-6.31 | 79,937,888 | 4 |
| E-510 | 55.6-62.3 us | 407.18-414.78 ms | 1602.3-1665.0 ms | 6.00-6.23 | 79,937,888 | 4 |

```sh
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test -tags=integration -run='^$' \
  -bench='^BenchmarkReference(Open|Decode|Render)$' -benchmem -benchtime=300ms -count=3 .
```

Render exceeds 300 ms, so each repeat measures a complete crop. Explicit
render buffers remain `8*cropWidth*cropHeight + 24*cropWidth` bytes: one output
and three normalized rows, not a second oriented image or full float plane.
Go allocation rounding is not a buffer-budget or RSS guarantee. No sustained
median timing regression over 10% versus Phase 6 was observed; allocation
counts/bytes are unchanged. Timings are local observations, not CI hard gates.

## The remaining certification gate

```sh
go run ./tools/rendergolden check-certification
```

This first verifies committed artifacts, then currently **exits nonzero** and
reports all four camera/mode groups as `BLOCKED`, each with one distinct
input/source and at least two required. Unit tests require this block explicitly;
engineering CI must not be interpreted as a release/certification pass.
The command is a mechanical necessary-condition check, **not sufficient proof
of independently captured photographs or a substitute for human approval**.

To unblock each initial camera/mode:

1. Obtain another independently captured original with individual CC0 evidence
   or separately approved redistribution permission. A mirror, re-encoded or
   edited copy, or another frame extracted from the same file does not count.
2. Record source/capture provenance, original SHA-256, identity, exact mode,
   geometry, CFA, bit depth and firmware when available. Review distinct
   capture evidence, not just distinct bytes or archive IDs. Do not pool modes.
3. Explicitly approve and update manifest/license evidence. Non-CC0 evidence
   needs an explicitly reviewed workflow extension; the current tool refuses
   to assume another license is equivalent.
4. Generate pinned native observations and independent RAW goldens, investigate
   every mismatch, validate calibration-domain/profile scope and all metadata,
   preview, API parity and rendered regressions. Do not extend profiles or
   overwrite existing goldens merely to make a new file pass.
5. Review the expanded support matrix and section 11 of [the design](../DESIGN.md),
   run all required native/race/fuzz gates, and obtain release approval before
   tagging or distributing binaries.

The public archive was rechecked during Phase 7; only the same approved sample
per model was available. No replacement or second photo was fabricated,
downloaded without permission, or counted through a mirror. Repeated engineering
review resolved license/schema, complete-pixel, ReaderAt, output-publication and
fixture-count findings. Native amd64 CI also exposed an over-strict bit-exact
comparison of derived profile coefficients: the regression harness now applies
the documented numerical tolerance, with exact-boundary/nonfinite tests and
unchanged full-image one-code limits. Production code and frozen PNGs were not
changed to hide architecture roundoff. A local emulated Linux/amd64 execution
reproduced the failure, then passed all full-image/parity tests with zero RGB
differences after this harness correction; observed coefficient differences
were at most `4.45e-16`. This is executed emulation, not a native runner claim.
The external fixture gate remains open.
