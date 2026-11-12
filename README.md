# libraw-go

A planned pure-Go Olympus/OM System ORF subset of LibRaw.

**Current status: Phase 1 complete: Go module, public data/error/option
contracts, and bounded binary/TIFF primitives. There is no production ORF
opener, decoder, renderer, or supported-camera claim yet.**

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
- Seven `errors.Is` sentinels and a contextual `Error` compatible with
  `errors.As`, preserving underlying I/O failures and cancellation.
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

Future `Open`/`File` operations will borrow a caller-owned, unchanged
`io.ReaderAt`, not close it or buffer the entire file. Each operation will
have its own mutable decode state; concurrent calls will require a
concurrently readable input. `RawImage`/`Preview` results will be caller-owned.
No placeholder `Open`, `DecodeRaw`, or `Render` implementation is exposed:
recognition and traversal belong to Phase 2, decoders to Phases 3-4, rendering
to Phase 6. This avoids returning unsupported for every file as if opening
were implemented.

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

This is not a general TIFF image decoder, graph walker, or MakerNote parser.
Duplicate-tag policy, IFD cycles/depth, raw-IFD selection, and bounded
compatibility for the measured old Olympus short-directory framing belong
to Phase 2. A generic classic reader is deliberately not relaxed to accept
that variant without explicit framing policy.

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
is evidence for future layout implementation, **not camera certification**.
The design's two-fixtures-per-advertised-camera/mode release gate still
applies. Newer 12/14-bit codecs, other ORF signatures, packed formats,
high-resolution modes, ORI, and multi-frame reconstruction remain deferred.
