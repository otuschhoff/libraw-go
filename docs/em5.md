# Active target: Olympus OM-D E-M5

The owner narrowed current scope to the personal ORF type found locally:
**E-M5, CameraType2 S0036, 12-bit legacy Olympus predictive RAW**.
Other camera models and newer codecs are irrelevant to current implementation
work. Older public fixtures remain compatibility regressions, not a request
to acquire/certify additional cameras.

## Evidenced storage and metadata

- IIRO header and little-endian `OLYMPUS` MakerNotes.
- One stored RGGB Bayer plane, 4640x3472 samples, 12 meaningful bits.
- TIFF BitsPerSample=16 and Compression=1 do **not** mean uncompressed
  16-bit samples. The seven-byte legacy prefix and validated descriptors,
  identity, mode, CFA and depth select the existing predictive decoder.
- Active area: `(8,8)` through `(4616,3464)`, a 4608x3456 crop.
- Camera software tag observed as Version 1.7; firmware remains unknown.
- Normal, sequence/burst and bracket files each contain one stored plane.
  Sequence metadata is not evidence of stacked or multi-frame decoding.

The full inventory contains **26 exact SpecialMode/DriveMode tuples**:

1. `SpecialMode=0 0 0;DriveMode=0 0 0`.
2. `SpecialMode=2 n 0;DriveMode=1 n 0`, for `n=1..14`.
3. `SpecialMode=2 n 0;DriveMode=5 n 1`, for `n=1..5`.
4. Bracket counter pairs `(special n, drive m)` of `(1,3)`, `(1,4)`,
   `(1,5)`, `(2,4)`, `(2,5)` and `(3,5)`, with SpecialMode's other components
   `2,n,0` and DriveMode's other components `5,m,1`.

These are observed tuples, not permission to accept arbitrary frame counters,
bracketing codes, other geometries or E-M5 Mark II/III files. Unknown modes,
identities, geometry, CFA, bit depths and codec prefixes stay unsupported.

## Metadata parsing and calibration

The original failure at tag `0x0100` was in **ImageProcessing/WB_RBLevels**,
not the top-level TIFF image width. The E-M5 stores four SHORT values
`R,B,256,256`, whereas earlier fixtures store two. The parser accepts this
four-value form only for normalized Olympus/E-M5/S0036, validates the trailing
green-reference components, and preserves the existing `R/256`, `B/256` and
unit-green interpretation. Unevidenced green references are rejected, not
silently ignored or guessed. Two-value decoding retains existing behavior.

Per-file black levels and as-shot WB remain file-origin values. A narrowly
scoped LibRaw-derived profile fills missing white and color only:

- Saturation `0xfe1` = **4065**, marked `OriginProfile`, not an estimate.
- XYZ-to-camera table coefficients:
  `8380,-2630,-639,-2887,10725,2496,-627,1427,5438`.
- The existing D65 row normalization/inversion converts this table to the
  public camera-to-XYZ convention; numerical tests independently compare
  all nine final linear-sRGB coefficients with the pinned reference.
- Profile scope includes make/model, S0036, the evidenced mode tuples,
  legacy layout, 4640x3472 geometry and 12-bit domain.
- File calibration is never overwritten; no black/WB values are invented.

Source: LibRaw 0.21.4 commit
`9646d776c7c61976080a8f2be67928df0750493e`.
The pinned `src/tables/colordata.cpp` SHA-256 is
`8d86b2265d75193ce331ba3bd49ad16e7711483833b7e550b7ad45eb6b94436b`;
`src/metadata/olympus.cpp` SHA-256 is
`160a93b5284abe2171bec6eb9a4aeecf8b0e12ab568859cb3c9c157e16ef8995`.
Its ImageProcessing parser reads the first two WB values divided by 256.
The [third-party notices](../THIRD_PARTY.md) and LGPL-2.1-only terms apply.

## API and exports

No new production API or native dependency is introduced. Use `Open`,
`Metadata`, `Capabilities`, `Preview`, `DecodeRaw`, and standalone `Render`.
Supported files advertise RAW and default rendering independently within
limits. E-M5 default rendering needs no estimated-white consent.

```sh
CGO_ENABLED=0 go run ./cmd/orfinfo "$PHOTO"
CGO_ENABLED=0 go run ./cmd/orfexport -format raw -output sensor.u16le "$PHOTO"
CGO_ENABLED=0 go run ./cmd/orfexport -format preview -output preview.jpg "$PHOTO"
CGO_ENABLED=0 go run ./cmd/orfexport -format png -output developed.png "$PHOTO"
```

Outputs must be new files. RAW is headerless full-plane u16le, without crop,
WB, scaling or orientation; the PNG is an independently developed oriented
active crop, not an embedded JPEG. Ownership, quota, cancellation, exposure
and error contracts remain those in [rendering](rendering.md) and
[the engineering guide](release-readiness.md).

## Private-corpus validation

Personal photographs are **not redistribution-approved public fixtures**.
Neither originals, sample planes, previews, rendered images nor private paths
are committed or uploaded. Normal unit tests and public CI use synthetic
E-M5 metadata/selector tests and existing licensed compatibility fixtures.
They never inspect a home directory or invoke native tools.

Explicit local verification uses the already pinned development exporter:

```sh
CGO_ENABLED=0 go run ./tools/em5check \
  -root "$ORF_PRIVATE_DIR" \
  -reference "$LOCAL_REFERENCE_EXPORT" \
  -all -workers 4 > .cache/em5-validation.json
```

This command:

1. Inspects every ORF in the explicitly provided directory; rejects malformed,
   out-of-scope or unavailable files instead of silently skipping them.
2. Checks the complete initialized RAW plane against pinned LibRaw 0.21.4:
   every sample exactly, with total mismatch/max-error/first-coordinate
   diagnostics and exact extent, geometry, crop and valid-bit checks.
3. Independently checks native black, white and as-shot WB domains.
4. Selects up to two **distinct input hashes** per exact mode tuple, plus
   orientation representatives, for complete filesystem/in-memory metadata,
   capabilities, preview, RAW and rendered-pixel parity, ownership, alpha,
   crop/orientation shape and 16-bit PNG encoding.
5. Writes only per-worker RAW scratch files into a private OS temporary
   directory, removing them on success, error or cancellation. No photographs
   or rendered files are published. SIGINT cancels work and child references.
6. Emits aggregate JSON, without private paths or image data, recording
   files/modes, exact sample counts, distinct representative counts, SDK and
   platform. Duplicate copies do not become independent captures.

Without `-all`, every file is still metadata-checked but only selected
representatives undergo native/RAW checks. `-workers` is bounded to 1..8;
limits are per worker, so parallelism increases total memory. `-per-mode`
sets the maximum distinct render representatives, minimum two requested,
but a scarce mode is reported honestly with its available count.
This tool is validation, **not an automatic camera/release certification**.
Native libraries/executables remain development-only.

Some rare frame-counter tuples have duplicate copies rather than two distinct
captures. This is recorded rather than manufactured into fixture coverage.
Any future public fixture distribution needs explicit permissions and an
approved evidence workflow. The historical four-camera certification command
does not determine readiness for this newly narrowed target.

## Recorded validation

On macOS/arm64, Go 1.26.0, the exhaustive local run checked **all 3,303 ORFs**
and **53,211,594,240 sample comparisons** against the pinned native decoder:
zero sample differences, zero native data errors and no skipped files.
Counts include duplicate copies; they are not claims of unique captures.
All 26 exact tuples have metadata/RAW support. Fifty-one selected cases
passed full filesystem/in-memory rendering/preview/RAW ownership parity,
oriented crop/alpha checks and PNG encoding.

Go 1.27.3 independently scanned all 3,303 files and passed native RAW and
full parity checks for the same 51 representatives (**821,614,080 samples**).
An actual E-M5 native comparison and complete parity/render check also passed
under Go 1.26.0's race detector. Default `orfexport -format png` produced a
private 16-bit image successfully; its temporary output was removed.

The two scarce exact tuples are
`SpecialMode=2 1 0;DriveMode=5 5 1` and
`SpecialMode=2 14 0;DriveMode=1 14 0`, each with one distinct original among
the available copies. Their RAW files were checked, not skipped or described
as two independent captures. Frame-counter tuples are distinct evidence
combinations, not claims of distinct compression algorithms.

The native exporter executable SHA-256 matched its saved pinned build:
`c850a8fc030c3f33ce905bb850df6ca1d484a05069746cacfb383397b47154c3`.
Private image data was never added to the public manifest or frozen baselines.

The new bounded E-M5 metadata fuzzer passed **600 seconds / 8,686,251
executions**. Follow-up whole-parser and MakerNote fuzzing passed 60 seconds
each, **966,146** and **999,416** executions respectively. Ordinary min/current
Go tests, complete licensed-fixture integration, vet, CGO-disabled builds and
dependency/native-input audits pass. Root coverage is **91.9%**.

Executed local Linux/amd64 emulation passes complete integration and vet;
local Linux/386 is explicitly compile-only. Public CI additionally runs native
Linux/macOS amd64/arm64 integration/races, actual 386 execution and now **eight**
fuzz targets, including `FuzzEM5Metadata`. CI results attach to the committed
code; private data is never needed or uploaded by those jobs.
