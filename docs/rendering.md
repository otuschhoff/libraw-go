# Phase 6: calibrated deterministic development

Phase 6 implements `Render(ctx, raw, RenderOptions)` independently of RAW
decoding. It returns a closable `RenderedImage` exposing opaque `image.NRGBA64` pixels encoded as
sRGB. It is a basic bilinear developer, not LibRaw postprocessing parity,
automatic white balance, tone mapping, or camera-JPEG matching.

The owner approved the four narrowly scoped LibRaw-derived profiles below.
This does not waive the two-fixtures-per-advertised-camera/mode certification
gate. There is still only one photograph per camera. Phase 7's
[engineering/readiness report](release-readiness.md) now records examples and
rendered regressions; combined M1+M2 release/certification remains blocked.

## Profile and capability matrix

Profiles require the already validated layout selector, exact normalized make,
model, camera identity, capture mode, dimensions and 12-bit sensor domain.
They never infer a decoder from a model name. Different geometries, identities,
modes, layouts or depths do not acquire a profile.

| Model / identity | Layout / dimensions | Exact SpecialMode / DriveMode | White | Default render |
|---|---|---|---|---|
| E-1 / D4040 | word16le-high12 / 2624x1966 | `0 0 0` / `0 0` | 4095, **estimate** | Requires consent or white override |
| E-400 / S0009 | word16le-high12 / 3768x2840 | `0 0 0` / `0 0 0` | 4095, **estimate** | Requires consent or white override |
| E-410 / S0013 | legacy-predictive12 / 3720x2800 | `2 1 0` / `1 1 0` | 3946, profile | Available within limits |
| E-510 / S0010 | legacy-predictive12 / 3720x2800 | `0 0 0` / `0 0 0` | 3946, profile | Available within limits |

E-1/E-400's maximum is the reference's bit-depth default, **not measured
saturation**. It is `OriginEstimate`, with an explicit diagnostic and source
rationale. Zero-valued render options refuse it. `AllowEstimatedWhite: true`
is explicit permission; an explicit white override is an alternative.
The predictive cameras' 3946 is the pinned `colordata.cpp` value `0xf6a`.

Validated file calibration retains precedence over profile values. Profiles
add missing white/color only: they never invent black levels or as-shot WB
from one photograph. Both remain required from the file or caller. White
levels, black levels and pixels remain in the same sensor-code domain.
`Metadata` and `DecodeRaw` expose this provenance; raw sample codes and geometry
are unchanged. MakerNote matrices with unverified semantics remain unused.

`Capabilities().Raw` and `.Render` are independent. Render availability means
that default rendering is implemented with the file's resolved limits and
calibration, **not** that unread sensor data is valid. An unavailable render
capability has an actionable reason. Explicit overrides can enable a standalone
`Render` even when default rendering is unavailable. Rendering uses its own
`RenderOptions.Limits`; pass the same limits as `Open` when maintaining the same
policy across operations. Limits are per operation; an inherited/shared Budget
also bounds simultaneously live buffers, including retained RAW and rendered
output. See [in-process safety](in-process-safety.md) for lifetimes and migration.

## Options and ownership

```go
raw, err := file.DecodeRaw(ctx)
if err != nil {
    return err
}
rgb, err := libraw.Render(ctx, raw, libraw.RenderOptions{})
if err != nil {
    return err
}
_ = rgb // *image.NRGBA64, not an embedded preview
```

Zero options use the verified active area, file/profile calibration, as-shot
WB, zero EV and applied EXIF orientation. A zero rectangle selects the RAW's
active area; any other `ActiveArea` is an explicit sensor-coordinate crop.
Missing crop is an error, not an assumption that all margins are active.

Present fields in `RenderOptions.Calibration` explicitly replace corresponding
RAW calibration fields; absent fields retain the originals. Supply `Value`
fields with `OriginOverride`, a meaningful `Source`, and sensor-position
`Levels` or an explicitly defined `ColorTransform`. Overrides are validated,
not normalized into success on invalid values. As-shot WB may be replaced
explicitly, but there is no automatic-WB or generic color fallback.

`ExposureEV` multiplies linear output by `2^EV`. NaN, infinity and an
unrepresentable zero/infinite gain are invalid. `PreserveOrientation` opts out
of orientation, including when orientation is unavailable. An unknown
orientation otherwise requires an explicit decision rather than guessing.

RAW pixels, diagnostics, calibration and options are never changed or retained.
Every successful output has independent storage, including across concurrent
calls. Callers must not change the RAW/options while they are being rendered.
The RAW plane is not counted again in rendering's per-operation working quota,
but library-produced RAW remains charged to its inherited Budget until Close.

## Exact stage conventions

1. Validate regular Bayer CFA with exactly one each of R, G1, G2, B; red and
   blue must be diagonally opposite. Validate geometry, stride, sample-slice
   length, depth, crop, calibration and resource arithmetic before allocation.
   Padded caller-owned strides are allowed when `len(Pixels)==Stride*Height`.
2. Keep CFA phase anchored at the uncropped sensor origin. At position `(x,y)`,
   normalize `(sample-black[y&1][x&1])/(white[y&1][x&1]-black[y&1][x&1])`.
   Clamp negative normalized signal to zero. Validate only sites in the crop;
   sensor margins outside it do not participate.
3. Apply each position's WB gain divided by **G1's gain**. G1 is the reference
   green, wherever its CFA position lies; G2 retains its separate gain.
   Preserve highlight headroom rather than clipping at normalization or WB.
4. Preserve every known color sample. For missing colors, average only matching
   sites in the in-crop 3x3 neighborhood: axial green and diagonal opposite
   color at red/blue sites; axial red/blue at green sites. Borders use available
   neighbors, never duplicated/clamped coordinates or outside-crop samples.
   A crop smaller than 2x2 cannot supply every channel and returns
   `ErrUnsupported`. This policy was explicitly approved by the owner.
5. Apply `ColorTransform.CameraToXYZ` to white-balanced camera RGB, with rows
   XYZ and columns RGB. `ReferenceWhite` is positive XYZ with Y=1 (tolerance
   `1e-6`). If it is not D65, adapt using Bradford:
   `B^-1 * diag((B*D65)/(B*sourceWhite)) * B`. Nonpositive cone responses,
   nonfinite or numerically singular transforms are rejected.
6. Convert XYZ D65 to linear sRGB using the inverse of the standard
   RGB-to-XYZ matrix:

   ```text
   0.4124564  0.3575761  0.1804375
   0.2126729  0.7151522  0.0721750
   0.0193339  0.1191920  0.9503041
   ```

   D65 is `(0.95047,1,1.08883)`. Apply `2^EV` after color conversion.
7. Clip only now to `[0,1]`, then encode sRGB:
   `12.92*v` for `v<=0.0031308`, otherwise `1.055*v^(1/2.4)-0.055`.
   Quantize with `floor(65535*v+0.5)`. Write standard big-endian NRGBA64
   channels with alpha 65535.
8. Apply EXIF 1-8 by writing each pixel directly to its final destination.
   Mirrored variants are included. Orientations 5-8 swap output dimensions;
   output bounds always start at `(0,0)`.

No whole float RGB frame or second orientation image is allocated. Explicit
working buffers are exactly `8*cropWidth*cropHeight + 24*cropWidth` bytes: one
NRGBA64 output and three normalized float64 Bayer rows. Limits resolve using
the existing `OpenOptions` rules, including consistent metadata/preview/working
budgets; for very small custom working limits also lower those two budgets.
Platform-int checks apply even when callers raise quotas.

Cancellation is checked before planning, before allocation, between rows and
before success. Errors return a nil image, never a partial image. `errors.Is`
classifies missing/unusable calibration as `ErrCalibrationUnavailable`,
unsupported CFA/tiny crops as `ErrUnsupported`, invalid RAW shape/samples as
`ErrCorrupt`, bad overrides/options as `ErrInvalidOptions`, and quota/platform
failures as `ErrLimitExceeded`. Context cancellation/deadlines are retained.

## Verified profile derivation and provenance

The pinned LibRaw 0.21.4 `cam_xyz` observations are **XYZ-to-camera** coefficients,
despite their name. They must not be copied as camera-to-XYZ. Let `A` be their
3x3 matrix and `W` be D65. The approved transform is:

```text
cameraToXYZ = inverse(A) * diag(A*W)
```

This normalizes the unbalanced camera response at D65 before inversion, so
white-balanced `(1,1,1)` maps to D65. Coefficients reproduce the reference's
float32 `/10000` table conversion. Independent tests compare the resulting
camera-to-linear-sRGB matrix to all nine pinned `rgb_cam` coefficients for all
four fixtures within `1e-6`, and verify neutral-white mapping.

These are fixed single-reference-white profiles, not illuminant interpolation
or a reuse of arbitrary MakerNote matrices. As-shot/caller WB precedes them.
Color quality remains that of a basic single-profile developer.

Source archive SHA-256:
`6be43f19397e43214ff56aab056bf3ff4925ca14012ce5a1538a172406a09e63`.
Relevant pinned source SHA-256:

| File in LibRaw 0.21.4 | SHA-256 |
|---|---|
| `src/tables/colordata.cpp` | `8d86b2265d75193ce331ba3bd49ad16e7711483833b7e550b7ad45eb6b94436b` |
| `src/utils/utils_dcraw.cpp` | `f6c51fb979a9fe675199757be0cccdc36e326bde362875601c74a01acb6ed829` |
| `src/tables/colorconst.cpp` | `5d500ed6e8c019ce3b4b60bb83d72fc74004c2f9eab08a0030d92d4d1757cfdc` |

The owner approved these specific profiles on 2026-11-12. Attribution and the
elected LGPL terms are recorded in [THIRD_PARTY](../THIRD_PARTY.md).

## Validation and reproducibility

```sh
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test -tags=integration -count=1 ./...
GOTOOLCHAIN=go1.26.0 go test -race -tags=integration -count=1 ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go vet ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go build ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test -run='^$' -fuzz='^FuzzRender$' -fuzztime=600s -fuzzminimizetime=1s -parallel=1 .
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test -tags=integration -run='^$' -bench='BenchmarkReference(Open|Decode|Render)$' -benchmem -benchtime=300ms -count=3 .
```

Normal tests require no photographs, network, LibRaw or ExifTool. Integration
uses the unchanged licensed/hash-checked M1 fixtures and goldens. It renders all
four full-resolution crops, checks all output alphas and shapes, verifies 257
independently calculated sensor-coordinate probes per image within one uint16
code, and verifies RAW ownership and estimated-white gating. All 36,691,904
RAW samples must still match exactly. Phase 7 adds committed end-to-end
rendered regression artifacts and ReaderAt parity without regenerating the RAW
oracle from this code; see the readiness report for its separate measurements.

Independent stage tests use `1e-6` absolute tolerance, with at most one uint16
code for final quantization. They cover all four CFA phases, odd crops, G1/G2
gain handling, headroom, published D50-to-D65 Bradford coefficients, every EXIF
orientation, 2x2/odd/narrow images, crop isolation, invalid inputs, numerical
extremes, exact quotas, allocation counts independent of height, platform-int
boundaries without large allocations, cancellation and concurrent ownership.
The portable CI runs the same integration/stage tests and races on native
Linux/macOS amd64/arm64, actual Linux/386 tests and seven fuzz targets.

### Recorded Phase 6 validation

Go 1.26.0 macOS/arm64 integration, race integration, vet and CGO-disabled build
pass; Go 1.27.3 integration also passes. Root-package statement coverage is
91.8%, aggregate instrumented coverage 91.0%. Demosaic, all orientation mappings,
regular-Bayer validation and sRGB encoding have 100% statement coverage.

The new renderer completed **600 seconds of fuzzing, 15,079,568 executions,
without failure**, using Go 1.26.0, `CGO_ENABLED=0`, `GOMAXPROCS=2`, one worker.
After profile/capability integration, parser and MakerNote fuzzing also passed
60 seconds each, with 1,236,212 and 1,281,257 executions respectively.
The prior M1 ten-minute gates remain recorded in the historical M1 report;
these new runs do not misrepresent 60 seconds as a repeated M1 long gate.

An isolated network-disabled Linux/arm64 Go 1.26.0 container passes integration,
vet, build, reference-tool tests and all original/artifact/layout/sample-hash
verification. Its Linux/386 checks are **compile-only**; native CI separately
executes the real 386 suite. No fixture or RAW oracle was regenerated.

Three unloaded 300-ms benchmark repeats, Apple M1 Max, macOS/arm64,
Go 1.26.0 and `CGO_ENABLED=0`, recorded:

| Fixture | Time/open | Time/RAW decode | Time/render | Render MP/s | Render B/op | Render allocs/op |
|---|---:|---:|---:|---:|---:|---:|
| E-1 | 62.9-66.6 us | 7.90-8.02 ms | 778-819 ms | 6.00-6.32 | 39,387,488 | 4 |
| E-400 | 70.2-71.0 us | 15.76-15.86 ms | 1556-1560 ms | 6.40-6.41 | 79,937,888 | 4 |
| E-410 | 55.0-63.6 us | 387.6-393.7 ms | 1575-1578 ms | 6.33-6.34 | 79,937,888 | 4 |
| E-510 | 55.1-57.6 us | 402.7-407.6 ms | 1576-1583 ms | 6.31-6.33 | 79,937,888 | 4 |

Render exceeds 300 ms, so each repeat measures one full cropped image. Input
hashes, full sensor dimensions, crop and orientation are printed by benchmarks.
Go-reported bytes include allocation rounding/container overhead; the explicit
buffer budget above is not a runtime RSS guarantee. Output is written directly
in its oriented form, without an extra image allocation. These local results
are observations, not portable performance guarantees.

Relative to the Phase 5 measurements on this same machine/toolchain, no
sustained median parse/RAW timing increase above 10% was observed. Profiles and
render-capability checks add small parse allocations; predictive RAW output
needs fewer diagnostic allocations once missing white/color are supplied.
The raw decoders themselves are unchanged.

Repeated completeness review checked the matrix direction/normalization,
estimated-white policy, exact profile scope, no invented black/WB, every option's
input/output ownership, odd-origin/border isolation, all mirrored orientations,
numeric underflow/overflow rejection, cancellation before allocation/final
success, quotas and 32-bit sizing. No outstanding Phase 6 implementation findings
remain. Phase 7 engineering is recorded separately; release/certification
continues to require additional independent licensed originals.
