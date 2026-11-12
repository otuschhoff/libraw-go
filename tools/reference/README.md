# Development-only ORF reference workflow

This tool implements Phase 0 evidence acquisition, not the production RAW
decoder. All Go code uses the standard library and runs by filename without
a production `go.mod`.

## Pinned inputs and prerequisites

[lock.json](lock.json) pins archive SHA-256 values, source revisions, versions,
and native build options for LibRaw 0.21.4 and ExifTool 13.40. The LibRaw
release's legacy decoder source hash was independently compared with the
file at its pinned Git revision.

Use **Go 1.26.0** for golden generation. Although Go 1.27 produces identical
sensor bytes and JSON for this corpus, its gzip encoder produces different
compressed bytes. A pinned Go generator makes every artifact hash
reproducible, not just its decompressed meaning. Production's minimum is Go
1.26; this development-tool pin is not a production toolchain ceiling.

Also required: a C++11 compiler available as `c++`, a C compiler, `make`,
Perl, and the platform's ordinary native build utilities. The native build
disables shared libraries, OpenMP, JPEG, Jasper, zlib, LCMS, and examples.
No native package is installed globally. The exporter statically links the
just-built pinned library rather than a system/homebrew LibRaw.

## Commands

Run from the repository root:

```sh
# Explicit network acquisition, with hash checks even for cached inputs.
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go run tools/reference/main.go fetch

# Offline build from the verified archives, in a fresh temporary tree.
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go run tools/reference/main.go build

# Offline non-native fixture/artifact/layout verification.
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go run tools/reference/main.go verify

# Two new native exports; both must match the committed artifact index.
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go run tools/reference/main.go reproduce

# Offline unit tests, including malformed inputs and failed downloads.
CGO_ENABLED=0 go test tools/reference/main.go tools/reference/main_test.go
go vet tools/reference/main.go tools/reference/main_test.go
```

`fetch` never replaces a corrupt existing cache entry silently: it reports
the expected and actual hash. Investigate and remove only that named entry
before retrying. HTTP 200 responses are not trusted until their content hash
matches. Downloads and archive extraction have finite byte budgets; archives
cannot escape their pinned root or install links.

`build` records compiler/platform/options and executable/source hashes in
ignored `.cache/reference/build.json`, with native configure/make logs beside
it. Source, lock, and executable freshness are checked before generation.
Unchanged trusted archives are used for each fresh build. The native upstream
may emit compiler deprecation warnings; exporter code itself uses
`-Wall -Wextra -Werror`.

`verify` requires all manifest photographs and committed artifacts. Missing
or changed required files are errors, never skipped tests. It checks license
evidence, input hashes, artifact hashes/lengths, decompressed sample
hashes/counts, and newly measured layout observations.

`reproduce` never edits the baseline. Its temporary exports are removed when
it finishes, including on failure. To investigate a discrepancy, explicitly
generate into a new directory:

```sh
GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go run tools/reference/main.go generate .cache/reference/candidate
```

The output directory must not already exist. Review differences and their
cause before replacing any golden data. Never make a production decoder
failure disappear by regenerating expected results from that decoder.

## Output format, version 1

For each fixture:

- `ID.json`: canonical JSON with input hash, reference revisions, lock/source
  hashes, exporter version, generator version, LibRaw unpack observations,
  and selected independent ExifTool tag observations.
- `ID.layout.json`: measured ORF header, IFD0 dimensions/organization, every
  logical strip offset/count, first payload bytes, and MakerNote location/header.
- `ID.u16le.gz`: deterministic gzip of row-major little-endian unsigned
  16-bit samples, with no internal header or row padding. Samples are sensor
  codes, not values scaled to fill uint16.
- `index.json`: per-input hash, uncompressed sample count/hash, and each
  artifact's byte length/SHA-256. Word-layout fixtures additionally record
  independently measured mismatch and nonzero-low-nibble counts.

The golden corpus is approximately 50 MiB compressed. It is intentionally
full-frame rather than a rendered-image proxy or a few spot checks.
The input photographs themselves are not checked in.

### Native export contract

[export.cpp](export.cpp) uses only `open_file` and `unpack`, not raw2image or
dcraw processing. RawSpeed and DNG SDK use, options, specials, and shot
selection are explicitly fixed. All reported native data errors fail export.
No black subtraction, WB, demosaic, orientation, or color transform is applied.

The exporter accepts only the two audited native decoder names. For the word
decoder, it exports `raw_width * raw_height` initialized samples. For the
legacy decoder, it exports the initialized rectangle from `(0,0)` through
the pre-unpack `width,height`, while reporting the allocation's raw dimensions
and byte pitch separately. All four initial fixtures happen to cover the
entire raw plane. Extra malloc rows or stride padding are never exported.
Any new decoder requires an audited initialized-region rule first.

The LibRaw fields are observations, not a new public Go API:

- `black` and `cblack` retain LibRaw's split scalar/channel/grid representation;
  the full `cblack` array is preserved, including grid dimensions.
- `data_maximum` is not computed by this workflow; zero is not the measured
  maximum. `sample_min`/`sample_max` are measured during export.
- `maximum`, WB, and matrices may include LibRaw camera-table fallbacks.
  Their presence does not establish that the ORF contains that calibration.
- `inset_crop` is `[left,top,width,height]`, distinct from LibRaw margins.
- `libraw_flip` is LibRaw's transform code, not an EXIF orientation number.
  The original numeric EXIF orientation is retained separately.
- `libraw_color_at_00_01_10_11` uses LibRaw's color indices and `cdesc`.
  All initial margins are zero, so its origin agrees with raw coordinates.
- `raw_masks` reports native mask rectangles without treating allocated
  padding as valid data.
- Native make/model may be normalized. ExifTool preserves the original
  string values for comparison.

Filesystem paths, access dates, build timestamps, serial numbers, and
executable addresses are deliberately absent from committed metadata.
Photograph capture/exposure metadata is retained where selected.

### Evidence instrument limitations

The Go inspector deliberately recognizes only the measured `IIRO` header and
the numeric IFD0/ExifIFD fields needed for this corpus. It is not the future
TIFF parser and does not claim support for arbitrary MakerNotes or ORFs.
ExifTool supplies the independent nested MakerNote interpretation.
Its `-Validate` diagnostics are retained, and family-4 instance groups keep
duplicate tag/warning names distinct rather than losing them in a JSON map.
The older fixtures exhibit known short-directory-length warnings; those
observations are documented in the layout notes, not hidden or repaired.

The word-layout measurement reads every stored little-endian word and checks
that a four-bit right shift equals every reference sensor sample. It also
counts nonzero discarded low nibbles. It does **not** reject a file merely
because those low nibbles are nonzero.

Reproducibility was tested on macOS/arm64 with Apple clang 21.0.0. No claim is
made here of bit-identical floating-point calibration metadata across
different native compiler/libm combinations. Cross-platform production
decoder tests belong to later phases; preserve and explain any reference
environment difference instead of weakening exact raw-sample assertions.
