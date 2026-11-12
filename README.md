# libraw-go

A pure-Go Olympus/OM System ORF subset of LibRaw under development.

**Current status: Phase 3 complete: exact sensor-code decoding for the evidenced
`olympus-word16le-high12` layout, alongside ORF metadata, embedded JPEGs and
`orfinfo`. Legacy predictive decoding awaits Phase 4; rendering is not
implemented. Layout coverage is not camera/mode certification or an M1 release.**

## Project decisions

The owner approved these decisions during Phase 0:

- Module import path: `github.com/otuschhoff/libraw-go`.
- Minimum production Go version: Go 1.26.
- Project license: LGPL-2.1-only; see [LICENSE](LICENSE) and the companion
  [GPL terms](COPYING.GPL).
- A properly attributed, license-compliant LibRaw-derived implementation is
  permitted. This project is not represented as a clean-room implementation.

The production module is initialized at the approved path and Go version.
It depends only on the standard library and builds with `CGO_ENABLED=0`.
Native tools below remain development-only, with no native or
external-process production dependency.

## Phase 3: evidenced word-based RAW decoding

`DecodeRaw` now implements the single initial evidenced word layout. No packed,
big-endian, interleaved, or additional camera layout is invented to satisfy a
generic "12-bit" label. For a `File` opened as in the next section:

```go
raw, err := file.DecodeRaw(context.Background())
if err != nil {
    return err
}
_ = raw.Pixels // caller-owned, full unrotated sensor plane, not RGB
```

The decoder reads one row buffer through the validated logical strips. Each
little-endian uint16 storage word is shifted right by four, yielding a 12-bit
sensor code. Nonzero discarded low nibbles are valid, not corrupt samples.
There is no crop, row shuffle, orientation transform, black subtraction, WB,
clipping, scaling, demosaic, or JPEG fallback.

The initial evidence gate compares **every sample**, including sensor margins:

| Required fixture | Sensor dimensions | Strips | Final strip rows | Exact samples |
|---|---:|---:|---:|---:|
| olympus-e-1-1800 | 2624 x 1966 | 16 | 16 | 5,158,784 |
| olympus-e-400-2151 | 3768 x 2840 | 89 | 24 | 10,701,120 |

All **15,859,904** samples match the pinned LibRaw 0.21.4 goldens, with zero
mismatches. Compressed-artifact SHA-256, full sensor u16le SHA-256, gzip trailer,
sample counts, export region, geometry and calibration propagation are checked.
The E-400 input's 793 nonzero low nibbles are retained in the source and correctly
discarded only by the documented storage-to-sensor conversion. Original inputs
and golden/reference artifacts are unchanged.

`RawImage.Stride == Width` and `len(Pixels) == Width*Height`. CFA and calibration
remain anchored to uncropped raw coordinate `(0,0)`; EXIF orientation is preserved
without rotating pixels. A verified active crop is returned as a half-open
rectangle without applying it. If no verified crop is available, `ActiveArea`
is empty with an `active-area-unavailable` diagnostic; the full sensor plane is
still decoded. Missing black/WB/white/color calibration retains its explicit
presence/provenance and does not prevent raw extraction.

### Capabilities, limits and failure behavior

`Capabilities.Raw.Available` is true only for the evidenced word selector when
its decode buffers fit the resolved limits. It does not merely test the camera
name or nominal bit depth. `orfinfo` reports this automatically without decoding
the image. Predictive/newer codecs and unsupported organizations return
`ErrUnsupported`; an identified word layout exceeding its working quota instead
returns `ErrLimitExceeded`, with an explanatory unavailable capability reason.
Rendering remains unavailable for every file.

Word plans require matching make/model/identity, MakerNote form, capture mode,
CFA, valid/storage depths, and exact contiguous row-sized strip organization.
Physically reordered/gapped strips, row padding, signed/floating sample formats,
nondefault predictors/fill orders/planar configuration/image depth, and other
packing are not silently interpreted as plain words. Malformed field types or
references fail explicitly.

Before allocation, the operation checks platform-sized byte/element counts and
the sum of one output plane, one input row, and diagnostic storage:
`2*width*height + 2*width + 64*diagnosticCount` accounted bytes. The diagnostic
allowance includes the unknown-crop diagnostic if needed. No second plane or
whole-file buffer is allocated. Retained `File` metadata and Go runtime
allocation rounding/GC/RSS are not per-call buffer-size guarantees.

Calls use independent output/scratch/diagnostic buffers and never close the
input. Concurrent calls require a concurrent `ReaderAt`, and their quotas are
per call, not a global concurrent-memory cap. Cancellation is checked between
rows and before success; the API cannot interrupt a blocked caller-supplied
`ReadAt`. Short reads, real errors (even combined with EOF on a full read), and
cancellation preserve their causes and never return a partially filled image.

### Phase 3 validation and repeated completion review

```sh
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test -tags=integration ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go vet ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go build ./...
GOTOOLCHAIN=go1.26.0 go test -race -tags=integration ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test -run='^$' -fuzz=FuzzDecodeWord -fuzztime=60s -fuzzminimizetime=1s -parallel=4 .
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test -run='^$' -fuzz=FuzzOpenORF -fuzztime=60s -fuzzminimizetime=1s -parallel=4 .
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test -tags=integration -run='^$' -bench=BenchmarkReferenceWordDecode -benchmem -count=3 .
```

The integration workflow uses the licensed originals fetched by the
[reference workflow](tools/reference/README.md), optionally under
`ORF_FIXTURE_DIR`. Missing/changed fixtures and goldens fail rather than skip.
Ordinary tests and synthetic fuzzing remain offline and photograph-independent.

Re-evaluation resolved newly relevant TIFF encoding controls, exact working-quota
capability boundaries, unknown crop semantics, and per-row error-position heap
allocations in the bounded reader. Error classifications and offsets are
preserved while successful reads no longer allocate a position per row.
Regression tests cover zero/max/alternating sensor codes, arbitrary low nibbles,
odd dimensions, final partial strips, truncated words/arrays, unsupported
organizations, literal/joined EOF, deadlines and cancellation on the final row,
independent metadata/calibration/pixel ownership, and concurrent calls.

Final validation on macOS/arm64:

- Go 1.26.0 offline/integration tests, vet and CGO-disabled builds pass;
  Go 1.27.3 integration tests and Go 1.26.0 race-enabled integration tests pass.
- Aggregate instrumented coverage: **91.1%**; word decode loop coverage: **100%**.
- Final full-parser/decode fuzz gate: **6,267,367 executions in 60 seconds**;
  arbitrary-word/geometry fuzz gate: **5,334,777 in 60 seconds**. No failures.
- Linux/386 integration tests cross-compile (`go test -exec=true`, not runtime
  execution); Linux/amd64, Linux/arm64 and macOS/amd64 production packages build.
- Production dependencies remain standard-library-only without CgoFiles or
  CXXFiles. Standalone reference tests and all four original fixture/artifact/
  layout/raw-hash integrity checks pass.

Go 1.26.0, `CGO_ENABLED=0`, Apple M1 Max, macOS/arm64; three 300ms benchmark
repeats with the fixed input hashes in the manifest measured:

| Fixture | Time/decode | Throughput | Go-reported B/op | Allocations/op |
|---|---:|---:|---:|---:|
| E-1, 2624 x 1966 | 7.6-8.9 ms | 581-677 MP/s | 10,328,078-10,328,205 | 4 |
| E-400, 3768 x 2840 | 15.1-15.5 ms | 690-709 MP/s | 21,414,666-21,414,671 | 4 |

These are local baseline observations, not machine-independent performance
promises. Allocation count is independent of row/strip count; a regression test
guards this property. The Phase 3 gate is complete with no outstanding findings;
Phase 4 predictive decoding and Phase 5 release/hardening gates remain separate.

## Phase 2: ORF metadata and embedded previews

```go
input, err := os.Open(path)
if err != nil {
    return err
}
defer input.Close()
info, err := input.Stat()
if err != nil {
    return err
}
file, err := libraw.Open(input, info.Size(), libraw.OpenOptions{})
if err != nil {
    return err
}
metadata := file.Metadata() // defensive snapshot; geometry is sensor-native
capabilities := file.Capabilities()
_ = metadata
if capabilities.Preview.Available {
    preview, err := file.Preview(context.Background())
    if err != nil {
        return err
    }
    _ = preview.JPEG // original bytes, not a sensor decode or a re-encode
}
```

`Open` borrows the caller-owned, unchanged `io.ReaderAt`. It never closes it,
buffers the whole file, allocates a raw plane, or decodes JPEG pixels.
Concurrent operations require concurrent-read support from that reader.
Every metadata/preview result is independently caller-owned.

The implemented parsing surface is deliberately narrow:

- The evidenced little-endian `IIRO` ORF header. `IIRS` and `MMOR` are recognized
  as unsupported variants; standard TIFF and unrelated bytes are not ORF.
- Classic image IFD/next-IFD/SubIFD traversal, EXIF, and the measured old `OLYMP`
  and new little-endian `OLYMPUS` MakerNotes. Offset bases are explicit.
- Bounded compatibility for old Olympus subdirectory lengths omitting exactly
  their two-byte count, with a diagnostic. Referenced metadata is file-bounded,
  not incorrectly confined to the MakerNote tag's declared blob.
- Original make/model/software strings, normalized camera identity, original
  EXIF orientation, sensor/active geometry, CFA at raw origin, valid bit depth,
  exposure, aperture, ISO, focal length, capture-mode fields, black and WB.
- Immutable plans for the evidenced E-1/E-400 word layout and E-410/E-510
  legacy predictive layout, requiring matching structural, identity, bit-depth,
  CFA and mode evidence. Compressed prefix evidence is checked separately.
  `Metadata.Layout` records the selector's provenance; it does not enable RAW.
- Aggregate directory/depth/entry/metadata quotas and independent payload,
  preview, dimension, platform-index, and working-buffer checks. Cycles,
  aliases, duplicate tags, ambiguous sensor IFDs, overlapping payloads/tables,
  and conflicting known calibration fail explicitly.

Safely parsed unsupported models, modes, tiled organizations, reordered/gapped
strips, or newer codecs can still expose metadata/previews. No camera-name-only,
file-size-only, or nominal-bit-depth-only decoder guess is made. Logical strip
order is preserved even when that organization cannot select an initial plan.
Phase 2 introduced metadata-only plans. Phase 3 now enables RAW only for the
word layout above; predictive/newer codecs still return `ErrUnsupported`.
`.Render.Available` remains false, and RAW never substitutes a JPEG.

White level, firmware, and camera-to-XYZ transforms remain explicitly unavailable
unless established by verified metadata; native reference camera-table values
are not relabeled as file calibration. Missing white/color calibration does not
prevent metadata, preview or supported raw extraction.

### Preview selection and errors

TIFF JPEG offset/length pairs and Olympus CameraSettings previews use their
evidenced offset bases. Candidates must fit the file, preview/working quotas,
and dimension/pixel limits, have SOI/terminal EOI markers, and contain a supported
JPEG frame header. `Open` examines headers, **not entropy-coded pixels**; this
does not certify the full JPEG stream. Selection is deterministic: largest
pixel area, then largest byte length, then lowest absolute offset.

`Preview(ctx)` separately copies the selected original bytes with bounded reads
and cancellation checks. `ErrNoPreview` is the documented typed outcome when
no candidate is present, valid, or within configured limits; metadata diagnostics
describe invalid/over-limit candidates. Structurally invalid references and real
I/O failures are not swallowed. `errors.Is` preserves cancellation and reader
failures, and no partial preview escapes on failure.

Orientation applies to embedded JPEG pixels, not implicitly to sensor pixels.
An orientation in the preview's own TIFF IFD is preserved. The measured Olympus
CameraSettings previews have no independently verified display transform, so
their returned orientation remains unavailable with a diagnostic; the library
does not guess that the sensor orientation applies.

### Deterministic diagnostic command

```sh
CGO_ENABLED=0 go run ./cmd/orfinfo path/to/input.orf
```

Output is versioned JSON (`SchemaVersion: 1`, `Metadata`, `Capabilities`) with
stable field/slice ordering, no timestamps or environment-dependent filenames,
and explicit value provenance and capability reasons. Usage errors exit 2;
file, parser, and output errors exit 1 with diagnostics on stderr. A safely
opened metadata-only unsupported file exits 0 but cannot claim raw capability.

### Phase 2 validation and completion review

Ordinary tests use synthetic, small ORFs/JPEGs and need no native tools, network,
or photographs. The separate required-corpus gate is explicit and **fails**
rather than skips when fixtures are missing:

```sh
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test -tags=integration ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go vet ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go build ./...
GOTOOLCHAIN=go1.26.0 go test -race -tags=integration ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test -run='^$' -fuzz=FuzzOpenORF -fuzztime=60s -fuzzminimizetime=1s -parallel=4 .
```

Fetch the licensed originals using the [reference workflow](tools/reference/README.md)
before running integration tests. `ORF_FIXTURE_DIR` optionally specifies their
directory; otherwise `.cache/fixtures` is used. The gate checks every input's
SHA-256 before comparing all four metadata sets with pinned ExifTool/LibRaw
expectations and JPEG bytes at independent reference offsets. A read spy ensures
opening/extraction never reads raw pixels (only the seven-byte compressed prefix
where required). Golden/reference artifacts are unchanged.

Repeated re-evaluation resolved classic-reader error precedence, exact old-block
compatibility, metadata string preservation, platform allocation checks,
payload/metadata/preview overlap, logical versus physical strip order, independent
JPEG scratch limits, no-preview error semantics, cancellation at operation
completion, conservative map/extent-storage accounting, and unknown preview
orientation. Tests also cover contradictory
calibration, unsupported codec prefixes/modes, invalid JPEG headers, snapshot
ownership, concurrent calls, and command output failures.

Final validation on macOS/arm64:

- Go 1.26.0: offline and integration tests, vet, and CGO-disabled builds pass.
- Go 1.27.3 integration tests and Go 1.26.0 race-enabled integration tests pass.
  CGO is enabled only for the race runtime in that check.
- Aggregate instrumented statement coverage (`-coverpkg=./...`): **91.0%**.
- Final full-parser fuzz gate: **6,596,594 executions in 60 seconds**;
  TIFF primitive regression fuzz gate: **5,832,907 in 30 seconds**. No failures.
- Linux/386 tests, including integration tests, cross-compile with
  `go test -exec=true`; this does **not** claim Linux runtime execution.
  Linux/amd64 production packages build.
- Production dependency inspection finds only the standard library and no
  CgoFiles/CXXFiles. Standalone reference tests and all four offline
  fixture/artifact/layout/hash integrity checks pass unchanged.

The Phase 2 gate covers only parsing/planning and preview extraction.
Two fixtures per initial layout are evidence, not camera/mode certification:
there is still only one file per camera. Phase 3 adds word sample parity;
predictive sample parity and M1 release gates remain for Phases 4-5.

## Phase 1 contracts

The root `libraw` package provides:

- `Metadata`, `Geometry`, `RawImage`, `Preview`, and per-operation
  `Capabilities` data contracts. Their presence does not claim that a file
  operation is implemented.
- `Value[T]` with explicit origin and source, so missing calibration is
  distinct from a legitimate zero. Camera-to-XYZ transforms explicitly state
  matrix direction, reference white, and indexing.
- Raw-origin 2x2 `CFA`/`Levels` types with distinct green positions, original
  EXIF `Orientation` values, and structured nonfatal diagnostics.
- `Metadata.Clone` for independent snapshots, including diagnostic slices.
- Eight `errors.Is` sentinels (including Phase 2's `ErrNoPreview`) and a
  contextual `Error` compatible with `errors.As`, preserving underlying
  I/O failures and cancellation.
- `OpenOptions.Resolve` and `DefaultLimits` for validated, value-owned
  resource settings. Zero fields use defaults; negatives and contradictory
  depth/IFD or working-buffer budgets fail explicitly.

For example, options can be validated without opening a file:

```go
limits, err := (libraw.OpenOptions{
    Limits: libraw.Limits{Pixels: 50_000_000},
}).Resolve()
if err != nil {
    return err
}
_ = limits // supply these resolved budgets to later file operations
```

Default limits are 1 GiB input, 100 million pixels, 32768 pixels per dimension,
128 IFDs, depth 16, 4096 entries per IFD, 16 MiB materialized metadata, 64 MiB
preview, and 1 GiB accounted working buffers. Limits are per operation, not
process/RSS guarantees. Raising them never disables overflow or platform-int
checks. A lowered working-buffer budget must still cover the resolved
metadata and preview limits, and resolved depth must not exceed the IFD quota.

`Open`/`File` now implement the borrowed-input and ownership contracts described
above. `RawImage` is now produced for the evidenced word layout; `DecodeRaw`
explicitly reports unsupported remaining layouts rather than fabricating pixels
or preview fallbacks. No `Render` implementation is exposed before Phase 6.

### Internal primitives

- `internal/bounded`: checked uint64 addition/multiplication, checked
  conversion to platform `int`, explicit-base offsets, validated file extents,
  allocation budgets, and exact `ReaderAt` handling. Partial results never
  escape as successful buffers; real I/O errors remain discoverable.
- `internal/tiff`: one classic IFD at a time, explicit byte order and offset
  base, inline/out-of-line data, count/aggregate-metadata budgets, and typed
  unsigned/signed/rational/float/ASCII access. Numeric arrays are accessed by
  index without a second unbudgeted decoded-array allocation.
- `internal/fault`: shared classifications behind the public aliases, so
  internal parsers and the later root orchestration can share errors without
  import cycles.

The TIFF reader owns each returned entry's bytes; it is a sequential,
per-parse object with a cumulative materialization budget. Charges include
the on-disk entry table, conservative entry/IFD structure allowances, and
tag value bytes, and are not refunded on failure or discard. Unknown field
types, zero counts, invalid typed access, zero rational denominators,
nonfinite real values, and malformed ASCII fail explicitly.

The internal TIFF package is not a general image decoder or graph walker.
Phase 2 adds selective materialization while still validating every entry's
structure/extent, shared aggregate budgets, and explicitly selected
directory-without-next-pointer framing. The generic classic reader retains
its strict framing; the root ORF parser owns traversal/MakerNote policy.

### Build and test

```sh
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go vet ./...
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go build ./...
```

These production-module commands are offline, need no photographs/native
tools, and exclude the nested development reference module. See
[reference instructions](tools/reference/README.md) for its explicit file-list
test commands; C++ compilation is never part of ordinary Go testing.

Phase 1 tests cover defaults and exact budget boundaries, every negative
option, contradictory settings, metadata snapshot ownership, all error
classifications/causes, arithmetic overflow/platform indexing, short and
faulty readers, both byte orders, relative bases, inline/offset arrays,
signed/unsigned/real types, zero denominators, and malformed IFDs. The
representation allowance is checked against actual Go type sizes.
Architecture-specific 32-bit guards are in the tests; cross-compiling them
does not claim that they executed on macOS/arm64.

### Phase 1 completion review

The implementation was re-evaluated against every Phase 1 task. Findings
resolved before completion included isolating native reference files from
production Go package discovery, preserving real errors combined with EOF
even on a full read, accounting for cumulative entry/table/value storage,
and verifying that separately returned TIFF values do not alias the input or
one another. There are no unresolved Phase 1 completion findings.

Final validation on macOS/arm64:

- Go 1.26.0: CGO-disabled full tests, vet, and build pass.
- Go 1.27.3: CGO-disabled full tests pass.
- Go 1.26.0 race-enabled full tests pass. CGO is enabled only for the race
  runtime in that check, not for a production dependency.
- Aggregate instrumented statement coverage is 98.3%; the option/default and
  metadata snapshot contracts are fully covered.
- Final 30-second bounded-IFD fuzz pass: 3,151,316 executions, no failure.
- Linux/386 test binaries compile using `go test -exec=true ./...` with
  `GOOS=linux GOARCH=386 CGO_ENABLED=0`; this is **compile-only**, not a
  successful Linux runtime execution. Linux/amd64 production packages build.
- Production module/dependency inspection finds no third-party modules,
  CgoFiles, or CXXFiles. No dependency installation was needed.
- Existing standalone reference-tool tests and all four fixture/golden/layout
  integrity checks still pass; Phase 0 oracle files are unchanged.

The phase gate is satisfied without exposing fake file/decoder operations or
waiving later ORF parsing, real-camera, rendering, or hardening gates.

## Phase 0 deliverables

- [Design and phased implementation plan](DESIGN.md).
- [Measured layout and algorithm notes](docs/orf-layouts.md).
- [Evidence/support matrix and phase completion review](docs/phase0.md).
- [Fixture manifest](testdata/fixtures.json) and
  [per-file CC0 license evidence](testdata/provenance/raw-pixls-selected.json).
- Four full sensor-plane golden datasets, their metadata/layout observations,
  and a [SHA-256 index](testdata/reference/index.json).
- [Pinned development-only reference tools](tools/reference/README.md).
- [Third-party provenance and licensing notes](THIRD_PARTY.md).

## Reproduce the evidence

Run from the repository root. Go downloads the pinned reference-tool
toolchain if it is not already available:

```sh
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go run tools/reference/main.go fetch
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go run tools/reference/main.go build
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go run tools/reference/main.go verify
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go run tools/reference/main.go reproduce
```

`fetch` is the only command that downloads archives or photographs. It
requires explicit execution, validates every SHA-256, and preserves verified
inputs in ignored `.cache/` storage. Approximately 53 MiB of ORFs are fetched;
the four golden sample streams occupy approximately 50 MiB in the repository.
`build` requires a C++11 compiler, C compiler, `make`, and Perl. It does not
install anything globally. See the reference-tool instructions for details.

Offline unit tests need neither downloaded photographs nor native tools:

```sh
CGO_ENABLED=0 go test tools/reference/main.go tools/reference/main_test.go
go vet tools/reference/main.go tools/reference/main_test.go
```

After an explicit `fetch`, `verify` checks all committed goldens and measured
fixture layouts without invoking LibRaw or ExifTool. `reproduce` additionally
requires the native development tools and compares two fresh exports to the
committed index. It never rewrites expected results.

## Scope boundary

Initial evidence covers E-1/E-400 little-endian word storage and E-410/E-510
legacy predictive storage. There is only one photograph per camera, so this
is layout implementation evidence, **not camera certification**. The word
layout now decodes; legacy predictive storage remains planned for Phase 4.
The design's two-fixtures-per-advertised-camera/mode release gate still
applies. Newer 12/14-bit codecs, other ORF signatures, packed formats,
high-resolution modes, ORI, and multi-frame reconstruction remain deferred.
