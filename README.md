# libraw-go

A planned pure-Go Olympus/OM System ORF subset of LibRaw.

**Current status: Phase 0 evidence and reference tooling only. There is no
production Go decoder, rendering API, or supported-camera claim yet.**

## Project decisions

The owner approved these decisions during Phase 0:

- Module import path: `github.com/otuschhoff/libraw-go`.
- Minimum production Go version: Go 1.26.
- Project license: LGPL-2.1-only; see [LICENSE](LICENSE) and the companion
  [GPL terms](COPYING.GPL).
- A properly attributed, license-compliant LibRaw-derived implementation is
  permitted. This project is not represented as a clean-room implementation.

The production module is intentionally not initialized until Phase 1. Native
tools below are development-only; the future Go library must build and run
with `CGO_ENABLED=0`, with no native or external-process runtime dependency.

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
