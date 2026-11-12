# Pure-Go Olympus ORF Decoder: Design and Execution Specification

Status: implementation specification; Phase 0 evidence and reference tooling
are recorded in [the Phase 0 report](docs/phase0.md). Phase 1's module,
contracts, and bounded primitives are described in [README](README.md).
Phase 2 implements bounded ORF metadata, layout plans, JPEG extraction, and
the diagnostic CLI. Phase 3 implements the evidenced word16le-high12 sensor
decoder with exact sample parity. Phase 4 implements the evidenced legacy
predictive decoder with exact sample parity. Rendering, M1 release hardening
and camera certification remain later gates.

## 1. Objective

Build a Go library that provides the Olympus/OM System ORF subset of
LibRaw's functionality without linking to LibRaw or executing native code.
The primary deliverable is correct extraction of sensor samples and the
metadata needed to interpret them. A separate, deterministic rendering
pipeline will turn supported Bayer images into usable RGB images.

"Pure Go" means:

- The production library builds and runs with `CGO_ENABLED=0`.
- No C/C++, FFI, dynamic libraries, external decoder processes, assembly,
  or dependency on the `purego` FFI package.
- Prefer the Go standard library. Any dependency requires a documented need,
  license review, and successful CGO-disabled builds.
- Native LibRaw and ExifTool may be used in a separate development-only
  reference workflow. They must not be needed to run normal Go tests against
  checked-in golden results.

This is a LibRaw-inspired subset, not a source-compatible Go translation of
the entire LibRaw API and not a promise of identical rendered output.

### 1.1 Release milestones

**M1: raw decoding.** Read metadata, extract embedded previews, and decode
verified ORF layouts into exact, unprocessed Bayer samples.

**M2: basic image development.** Render M1 samples using explicit calibration,
white balance, bilinear demosaicing, color conversion, and sRGB encoding.

**M3: broader ORF coverage.** Add newer metadata-parameterized compression
and additional modes only after acquiring suitable evidence and fixtures.

M1 is independently useful and must ship before rendering is attempted.
The planned initial usable release includes M1 and M2 for a published,
fixture-backed subset of cameras and modes. M3 is a subsequent milestone,
not an implied claim of support in that release.

### 1.2 Success criteria

1. All supported raw samples match the pinned reference decoder exactly in
   the defined comparison region and numeric domain.
2. Geometry, CFA origin, calibration, and orientation have explicit contracts.
3. Unsupported files are rejected with actionable errors; the library never
   substitutes an embedded JPEG for decoded RAW data.
4. Malformed files do not cause panics, hangs, uncontrolled allocation, or
   reads outside validated input ranges.
5. Normal build and test workflows do not require native software or network
   access once the required small fixtures are available.
6. Every claimed camera/mode/layout combination has reproducible evidence.

## 2. Scope and support policy

### 2.1 Initial scope

- Olympus ORF containers and compatible OM System ORF containers when their
  actual storage layout is verified.
- Classic TIFF-style IFD structures with ORF-specific signatures.
- EXIF and the Olympus MakerNote subsets necessary for decoding and rendering.
- Verified single-frame, regular 2x2 Bayer sensor mosaics.
- Legacy predictive compressed ORF, normally associated with 12-bit samples.
- Verified uncompressed word-based and packed layouts used by selected older
  Olympus cameras.
- Embedded JPEG preview extraction as an explicit, separate operation.
- Sensor-native decoding with no implicit crop, rotation, scaling, or corrections.

These are layout families to implement, not assertions that every camera in
a family is supported. A filename extension, make string, nominal bit depth,
or compression tag by itself is insufficient to select a decoder.

### 2.2 Deferred or excluded

- Non-ORF formats, including TIFF images and DNG files from Olympus cameras.
- BigTIFF, arbitrary TIFF codecs, and an ORF encoder.
- High-resolution/pixel-shift modes, ORI companion files, multi-frame modes,
  and nonstandard CFA layouts until independently verified.
- Newer parameterized 12/14-bit compression until M3.
- Noise reduction, sharpening, lens corrections, highlight reconstruction,
  automatic brightness, local tone mapping, and manufacturer picture styles.
- Pixel-identical reproduction of LibRaw/dcraw postprocessing or camera JPEGs.
- Full EXIF/MakerNote coverage, lens databases, GUI, and general plugin systems.

The reader may expose metadata for recognized but undecodable ORF variants.
Its capability report must clearly distinguish metadata-only, preview,
raw-decode, and render support.

### 2.3 Support matrix

Maintain a machine-readable fixture manifest and a human-readable support
table. Each row identifies:

- Normalized and original make/model; camera identifier where available.
- Firmware and capture mode when known; otherwise explicitly unknown.
- Container signature, byte order, raw dimensions, bit depth, compression
  identifier, relevant MakerNote parameters, and strip/tile organization.
- Internal decoder/layout ID and supported operations.
- At least one fixture hash, reference version, and verification result.
- Restrictions and untested modes.

Use explicit named layout rules with evidence, not a broad model allowlist.
Unknown models may use an existing decoder only when a validated rule proves
that all required layout properties match. Otherwise return an unsupported
error. Never guess a layout by attempting decoders until one produces pixels.

## 3. Evidence, licensing, and reproducibility

### 3.1 Evidence policy

ORF is not simply standard TIFF with a different extension. Important
behavior is established by existing implementations and file measurements.
Treat this specification as architecture and acceptance requirements, not
as a complete byte-level ORF format definition.

Before implementing a layout, produce a concise algorithm/layout note with:

- Exact reference repository revision and relevant symbols.
- Fixture hashes and observed tag values.
- Byte order, bit order, offsets, row layout, predictor rules, and boundaries.
- Which details are measured, reference-derived, or still unknown.
- Test vectors covering each nontrivial branch.

Unknown behavior is a research task or an explicit unsupported case, never
permission for an LLM to invent constants, matrices, or decoding rules.

### 3.2 Licensing gate

Choose and record the project's license before implementation. LibRaw offers
LGPL-2.1 and CDDL-1.0 licensing choices; translating source does not remove
license obligations. Camera tables and calibration data also need provenance.

The design was informed by inspection of public reference implementations;
it must not be described as a clean-room specification. If a permissive,
independently authored implementation is required, obtain appropriate review
and define an acceptable implementation/evidence process before coding.
Alternatively, approve a compatible source-derived implementation with all
necessary attribution and compliance steps. Do not copy reference code or
calibration tables into the project while this decision is unresolved.

Only redistribute photographs, extracted data, and golden outputs where
their licenses or permissions allow it. Record attribution requirements.
Personal or nonredistributable samples stay outside the repository.

### 3.3 Reference workflow

Use a pinned LibRaw build as the raw-sample oracle, plus ExifTool as an
independent metadata reference. An offline development utility may use
LibRaw's native API, but lives outside the production dependency graph.

The reference exporter must:

1. Open and unpack the file without running a development/postprocessing stage.
2. Export raw-buffer dimensions, stride, initialized/meaningful region,
   active-area margins, CFA pattern/origin, orientation, bit depth, black
   levels, white level, white balance, and relevant color transforms.
3. Write row-major samples as explicitly little-endian uint16 values.
4. Record the input SHA-256, LibRaw commit, build options, exporter version,
   and any unpack flags that can change samples.
5. Identify any reference-only shift, crop, masked region, or value-domain
   conversion. Never compare uninitialized native-buffer padding.

Store metadata in versioned JSON and samples in a lossless, documented binary
format, optionally compressed with a standard-library-supported codec.
Do not use a rendered TIFF or PNG as a proxy for raw-sample parity.
Reference export must be deterministic and independently reproducible.

## 4. Public API and data contracts

Use package `libraw` at the module root. The module import path and minimum
Go version are owner/repository decisions made in phase 0, not invented here.
The following is the intended API shape; define supporting types and settle
exact names in phase 1 before exposing a stable release:

```go
func Open(r io.ReaderAt, size int64, opts OpenOptions) (*File, error)

func (f *File) Metadata() Metadata
func (f *File) Capabilities() Capabilities
func (f *File) DecodeRaw(ctx context.Context) (*RawImage, error)
func (f *File) Preview(ctx context.Context) (*Preview, error)

func Render(ctx context.Context, raw *RawImage, opts RenderOptions) (*image.NRGBA64, error)
```

### 4.1 Input and ownership

- `Open` parses bounded metadata and constructs a validated decode plan;
  it does not allocate the full raw image or decode the preview.
- The caller owns the `ReaderAt` and keeps it alive and unchanged for the
  lifetime of `File`. There is no library-owned descriptor to close.
- A `File` has immutable internal metadata. Metadata access returns values
  or defensive copies; returned slices must not expose internal state.
- Decode calls use independent state and buffers. Concurrent calls are
  supported when the supplied `ReaderAt` supports concurrent reads.
- Successful image and preview results are caller-owned, with no aliases to
  library caches. No global mutable decoder state.
- Do not add an `io.Reader` API that silently buffers the entire input.
- On failure, return no partially valid image or success-shaped fallback.

### 4.2 RawImage

Define `RawImage` with these semantics:

- `Pixels []uint16`: sensor-code values, not normalized to 16-bit range.
- `Width`, `Height`: meaningful decoded sensor-plane dimensions.
- `Stride`: measured in samples; initially exactly `Width`.
- `ActiveArea image.Rectangle`: valid active crop in sensor coordinates,
  using Go's half-open rectangle convention. Empty means unavailable, with
  a diagnostic; do not guess that the whole decoded plane is an active crop.
- `CFA`: a 2x2 pattern anchored at raw coordinate `(0, 0)`. Preserve separate
  green positions for calibration even though both render as green.
- `BitDepth`: nominal valid sample depth, separate from the storage word size.
- `Calibration`: explicit black, white, WB, and color-transform information.
- `Orientation`: unmodified EXIF orientation, including mirrored variants.
- `Diagnostics`: structured nonfatal limitations and metadata issues.

Keep stored payload geometry, decoded-plane geometry, and active geometry
separate internally. Some formats consume coded samples that are not useful
output pixels. Never invent zero-valued pixels merely to fill a reference
buffer's allocation dimensions.

Decoded samples have no black subtraction, WB, color conversion, rotation,
or undocumented shift applied. If storage codes require a defined conversion
to sensor-code values, document it as part of the layout and test it.
Valid samples outside the active area are retained where the layout supplies
them; undocumented reference padding is not part of the public image.

### 4.3 Metadata and calibration

Preserve original make/model strings alongside normalized identifiers.
Represent optional values with explicit presence/provenance, not ambiguous
zero values. At minimum include geometry, orientation, capture exposure,
ISO, focal length, camera WB, black/white levels, and available color data.

Calibration precedence is:

1. Validated per-file metadata with known semantics.
2. A documented, mode-specific camera profile with approved provenance.
3. Explicitly unavailable.

Use a four-position 2x2 black-level model with a stated origin. Keep black
levels, white levels, and pixels in the same numeric domain. Do not assume
white level equals `(1 << BitDepth) - 1`; that may be used only as a documented
estimate that is flagged as such and explicitly permitted by the caller.
Missing color calibration must not prevent exact RAW extraction.

### 4.4 Errors and diagnostics

Provide sentinel errors compatible with `errors.Is` for:

- Not an ORF container.
- Recognized but unsupported layout or mode.
- Corrupt structure or impossible sample data.
- Truncated input.
- Resource limit exceeded.
- Required rendering calibration unavailable.
- Invalid API options.
- No supported embedded JPEG preview (including candidates excluded by
  preview/working/dimension limits, with detailed metadata diagnostics).

Wrap errors with operation, tag/IFD or layout, and byte/bit position where
available. Preserve underlying I/O errors and context cancellation.
Unknown optional tags are skipped after validating their structure.
Malformed optional metadata may produce a structured diagnostic only when
it cannot affect the decode plan; required layout metadata is a hard error.
Library code does not print, log globally, recover broadly, or hide failures.

Metadata-only opening of an unsupported mode is allowed when safely parsed;
its capability report explains the limitation and `DecodeRaw` returns the
typed unsupported error.

## 5. Implementation architecture

Keep the first implementation small:

```text
go.mod
api.go, types.go, errors.go       Public contracts and orchestration
orf.go                          Recognition, metadata, decode-plan selection
internal/tiff/                  Bounded IFD reader and typed tag access
internal/olympus/               MakerNotes, layout rules, raw decoders
internal/bitio/                 Checked ORF bit reading
internal/render/                Deterministic M2 processing
cmd/orfinfo/                    Small metadata/capability diagnostic command
testdata/                       Licensed fixtures, manifests, golden results
tools/reference/                Offline oracle exporter and instructions
```

This is a responsibility map, not a requirement to create every file or
directory immediately. Avoid generic codec registries, plugin APIs, a full
TIFF framework, speculative parallelism, or one abstraction per function.
Production code must not import reference tooling.

### 5.1 Container and IFD parsing

1. Recognize bytes, not extensions. Enumerate and test accepted ORF header
   variants and their endian interpretation using phase-0 evidence.
2. Do not pass an ORF directly to `image/tiff` and assume standard TIFF rules.
3. Parse classic IFD entries using explicit byte order and checked arithmetic.
4. Support the TIFF scalar/array types needed by fixtures, including signed
   and unsigned rationals with explicit zero-denominator handling.
5. Distinguish inline values from out-of-line data, and follow only relevant
   IFD, SubIFD, EXIF, and MakerNote references.
6. Represent offset bases explicitly: file-relative, TIFF-header-relative,
   and MakerNote-relative offsets must not be mixed.
7. Track visited IFD locations and impose depth, count, tag, and byte budgets.
8. Select the sensor IFD by validated structural rules, not simply the
   largest image, first IFD, or highest byte count.
9. Keep preview IFDs separate. Validate each selected payload extent.

Strip offsets need not be physically ordered or contiguous. Preserve logical
strip order and validate matching offset/count arrays. Reject tiled or
multi-strip variants unless their reconstruction and codec restart semantics
are implemented and covered. Do not concatenate strips on assumption.

### 5.2 Olympus MakerNotes

Support only the MakerNote header variants and subdirectories observed in
fixtures. Parse byte order and offset bases per variant; nested directories
may use different bases from the parent TIFF.

Extract the fields needed for:

- Sensor dimensions, active crop, and valid sample depth.
- Bayer pattern and camera identity/mode.
- Black levels, white/saturation levels where available, and camera WB.
- Color data only when its direction and scaling are verified.
- Preview locations.
- Decoder parameters for newer compression in M3.

Validate field types and counts. Keep raw tag identifiers in diagnostics.
MakerNote crop fields do not necessarily share TIFF crop conventions;
convert to half-open sensor coordinates only after proving their semantics.
Do not treat every MakerNote field named "ColorMatrix" as a camera-to-sRGB
matrix.

### 5.3 Decode plan and layout dispatch

Construct an immutable plan containing:

- Layout ID, evidence-backed selector, and required metadata.
- Payload ranges and logical segment order.
- Storage dimensions, output dimensions, active area, and CFA origin.
- Storage bit depth, sensor-code depth, byte/bit order, row stride,
  packing/interleave details, and any defined value conversion.
- Codec header length, predictor parameters, and restart boundaries.
- Estimated output and scratch-memory requirements.

Reject contradictory metadata and ambiguous candidates. For example, a
payload size that happens to equal `width * height * 2` is supporting
evidence, not sufficient proof of uncompressed 16-bit storage.

### 5.4 Word-based and packed decoders

Implement a separate small routine per evidenced layout:

- Word endianness and effective valid bits.
- Exact packed sample ordering, including nibble order.
- Row padding/alignment and any row interleaving.
- Strip transitions and final partial groups.
- Model/mode-specific shifts or masks only when justified by evidence.

Use synthetic vectors containing zeros, maxima, alternating values, odd
dimensions, and row-boundary transitions. Compare real samples exactly.
Do not treat every older ORF as generic little-endian uint16 or generic
12-bit pairs; layout-specific details are part of the decoder.

### 5.5 Legacy predictive compressed decoder

The legacy LibRaw reference uses adaptive residual coding and prediction
from same-CFA neighbors. It is not JPEG and must not be sent to a JPEG codec.

Before coding, freeze an algorithm note specifying:

1. Payload prefix/header length and validation.
2. Bit order, lookahead behavior, and legal end-of-stream padding.
3. Residual sign and low-bit extraction.
4. Adaptive code-width selection, prefix/escape decoding, and state updates.
5. Per-column-parity adaptive state and its row reset rules.
6. Boundary predictors for the first two rows and columns.
7. Interior prediction using same-color neighbors two pixels away, including
   branch comparisons, thresholds, tie behavior, and signed arithmetic.
8. Consumption of coded columns outside the meaningful output width.
9. Sample reconstruction and legal range.

Use explicit signed integer widths for predictor/residual arithmetic and
document arithmetic-right-shift behavior. Validate shifts and widths before
executing them. Do not inherit C/C++ overflow, sign-conversion, or out-of-range
read behavior accidentally.

The bit reader must distinguish valid padding from missing data. Prefix and
escape parsing must have finite bounds; an all-zero malformed tail cannot
loop indefinitely. Never silently zero-fill an incomplete bitstream.

Decode serially first. Use row/local adaptive state and the output plane for
neighbor access; extra scratch space should be bounded by row size plus a
fixed input buffer. Poll cancellation at least once per row and bound all
inner loops. A decoder error invalidates the entire result.

### 5.6 Newer parameterized compression

Current LibRaw has a distinct Olympus/OM System metadata-driven 12/14-bit
implementation. Do not extend the legacy decoder by changing a bit-depth
constant. M3 must independently verify header rules, MakerNote parameters,
code-width limits, predictor/state behavior, and calibration scaling.

Until then, recognize applicable signatures/parameters where possible and
return unsupported. Do not misclassify these files as corrupt legacy streams.

### 5.7 Preview extraction

Select a documented embedded JPEG representation deterministically; prefer
the largest validated supported preview, and expose its dimensions and
orientation semantics. Return original JPEG bytes, not an implicit re-encode.

Bound preview reads. Missing preview is a documented typed outcome.
Successful preview extraction says nothing about RAW decode support.
The library does not use a preview to fulfill `DecodeRaw` or `Render`.

## 6. Resource and malformed-input policy

Expose limits in `OpenOptions` with these initial defaults:

| Limit | Default |
|---|---:|
| Input size | 1 GiB |
| Decoded pixels | 100,000,000 |
| Maximum width or height | 32,768 |
| Total visited IFDs, including MakerNotes | 128 |
| IFD nesting depth | 16 |
| Entries in any IFD | 4,096 |
| Aggregate materialized metadata | 16 MiB |
| Extracted preview bytes | 64 MiB |
| Accounted live working buffers per operation | 1 GiB |

Zero-valued options select defaults; negative or contradictory settings are
invalid. Allow callers to raise positive limits explicitly, but never bypass
integer/range checks. These are engineering defaults, not format limits.

- Use `uint64`/`int64` checked arithmetic for offsets, products, and additions.
  Verify representability before converting to `int`, especially on 32-bit Go.
- Require every extent to fit within the declared input size.
- Honor `ReaderAt` short reads and real I/O failures.
- Budget image, intermediate render buffers, metadata, and previews before
  allocation. The budget covers explicitly allocated buffers, not a promise
  about Go runtime RSS or garbage-collector overhead.
- Do not allocate based on unvalidated tag counts.
- Do not require the entire input file to reside in memory.
- No panic recovery as a substitute for validating input.
- Do not require the caller's `ReaderAt` to be interruptible. Cancellation is
  checked between bounded reads/rows; it cannot interrupt a blocked caller I/O.
- Document that limits are per operation, not a global concurrent-process cap.

## 7. M2 rendering contract

Keep rendering independent of decoding. Require a supported regular Bayer
pattern, valid geometry, and sufficient calibration. Missing required data
returns a calibration error unless the caller supplies explicit overrides.
No automatic-WB or arbitrary generic color-matrix fallback.

The baseline pipeline is:

1. Select the validated active area without losing raw-coordinate CFA phase.
2. For each CFA position, subtract its black level and divide by the matching
   white-minus-black range using floating-point intermediates.
3. Clamp negative normalized values to zero. Retain highlight headroom until
   the documented final gamut/output clipping step.
4. Apply camera/as-shot WB or caller-supplied gains. Specify the gain order
   and normalize the reference green gain to one.
5. Bilinearly demosaic, preserving known samples and averaging only valid
   neighboring sites at edges. Handle tiny images explicitly.
6. Transform linear camera RGB to XYZ and then linear sRGB, using a verified
   profile. State matrix direction, row/column convention, reference white,
   and chromatic adaptation; perform adaptation when needed.
7. Apply caller exposure compensation as `2^EV`, with default EV zero.
8. Clip to the output gamut, apply the standard piecewise sRGB transfer
   function, and quantize with documented rounding into `image.NRGBA64`.
   Output alpha is always fully opaque.
9. Apply all eight EXIF orientation transforms when the explicit render option
   requests it; otherwise retain sensor orientation. Default: apply orientation.

Color profiles must identify their supported camera/mode and provenance.
M2 may initially support fewer cameras than M1. If illuminant-dependent
profiles cannot be interpreted correctly, return unsupported calibration
rather than improvising an interpolation.

Rendering is deliberately basic, not a quality substitute for a mature RAW
developer. Do not require an arbitrary visual similarity score against a
camera JPEG. Use exact stage-level synthetic tests, explicit equations,
orientation/CFA tests, and approved project-owned output goldens.
Floating-point stage checks use a stated tolerance (initially `1e-6` absolute
for normalized test values); final quantized regression images permit at
most one 16-bit code value per channel across supported architectures.

## 8. Test and fixture strategy

### 8.1 Fixture corpus

Start with legally redistributable samples or contributor-owned photographs.
Public availability is not permission to redistribute. Raw sample archives
such as raw.pixls.us are discovery sources; verify each file's license.

Required coverage before claiming a layout:

- At least two real files per implemented layout, preferably from different
  models; document when a layout is known to exist on only one model.
- At least two files per advertised camera/mode combination.
- Low-signal and varied/high-signal scenes across the corpus.
- Both byte orders if claiming both, all supported header variants, and
  each implemented strip organization.
- At least one legacy compressed model and one verified older uncompressed
  or packed model in M1.
- Separate fixtures for each added special/newer mode in M3.

Use very small synthetic containers for parser and primitive tests. Synthetic
fixtures do not establish real-camera compatibility. Keep large licensed
integration files separate from the lightweight suite when necessary.

The manifest records fixture source/license/hash, expected capability,
layout ID, expected metadata, reference artifacts, and skip rationale.
Required release fixtures must not silently skip when missing. Optional local
corpora may skip with explicit reporting. Fetching is opt-in and hash-checked;
ordinary tests never download files.

### 8.2 Test layers

| Layer | Required checks |
|---|---|
| TIFF primitives | Byte orders, types, inline/offset values, rational errors |
| IFD graph | Nested offsets, cycles, duplicate/ambiguous critical tags, budgets |
| MakerNotes | Each header/base variant, calibration and crop semantics |
| Bit reader | Width boundaries, refills, EOF, padding, prefix/escape limits |
| Raw algorithms | Packing, state resets, predictor branches, range errors |
| Differential | Exact sample values, geometry, CFA, calibration provenance |
| Rendering | Per-stage math, four CFA phases, tiny edges, 8 orientations |
| API | Ownership, concurrency, cancellation, invalid limits, error wrapping |
| Fuzzing | Parser and decoder entry points; no panics, hangs, invalid allocations |
| Portability | CGO-disabled builds, endian-independent output, 32-bit checks |

Golden comparison reports the first mismatch coordinate, expected/actual
sample, mismatch count, and maximum absolute error. The acceptance threshold
for meaningful raw samples is **zero mismatches and zero absolute error**.
Any deliberate reference-domain conversion must be separately tested and
recorded; it cannot excuse unexplained mismatches.

Malformed cases include truncated headers/arrays/payloads, overflowed counts,
IFD cycles, wrong offset bases, contradictory dimensions, zero denominators,
invalid shifts/parameters, out-of-range samples, and endless prefix codes.

### 8.3 Performance

Benchmark parse, raw decode, and render separately with fixed fixture hashes.
Record Go version, architecture, CPU, fixture dimensions, allocations,
bytes/op, elapsed time, and raw decode megapixels/second.

Initial constraints:

- Raw decode allocates one output plane plus bounded input/row scratch, not
  a second full image or a whole-file buffer.
- Working buffers stay within the configured budget.
- Runtime work is linear in validated payload/sample count, including
  malformed streams through bounded prefix decoding.
- Timing regressions greater than 10% on the same benchmark environment need
  investigation across repeated runs; do not make noisy CI timing a hard gate.

Do not promise a machine-independent throughput target without measurements.
Optimize only after exact parity is established.

## 9. LLM execution protocol

Execute phases in dependency order. Each phase must leave a buildable,
reviewable result with tests and updated support documentation.

For every task:

1. Read the relevant existing code, contracts, fixture manifest, and layout note.
2. State the bounded change and acceptance test. Do not redesign unrelated code.
3. If a required fact or fixture is absent, mark the task blocked and identify
   the exact missing evidence. Do not infer success from a plausible image.
4. Add the smallest failing test or reference comparison that captures it.
5. Implement using explicit types, checked reads, and existing helpers.
6. Run targeted tests, then the required phase gate.
7. Record commands/results and supported/unsupported behavior in the delivery
   summary. Update persistent docs only where behavior or evidence changed.
8. Proceed only after the gate passes. Never regenerate golden outputs from
   the implementation being tested.

Do not implement speculative features, rewrite independent modules, silently
weaken assertions, or mark placeholder decoders supported. An unsupported
return is acceptable for deferred scope, not as completion of required M1 work.
Parallel tasks are appropriate only after shared contracts are fixed; avoid
concurrent edits to decoder dispatch, API contracts, or the same fixture data.

## 10. Implementation phases

### Phase 0: evidence, scope, and licensing

**Depends on:** nothing.

**Tasks:**

- Confirm module path, minimum supported Go release, and license strategy.
- Pin reference revisions/build options and review relevant license notices.
- Acquire the initial legal fixture corpus; create the hash/license manifest.
- Inspect headers, IFDs, MakerNotes, payload layouts, and modes.
- Write the initial variant/support matrix and layout notes.
- Build the development-only reference exporter and produce reproducible
  metadata/sample goldens.

**Gate:** at least one legacy compressed layout and one older word/packed
layout are evidenced; required fixtures have permissions and stable hashes;
repeated reference exports are byte-identical; license strategy is approved.
If fixture availability changes the achievable scope, obtain owner approval
instead of silently reducing M1.

### Phase 1: module, API, and bounded binary primitives

**Depends on:** phase 0.

**Tasks:**

- Initialize the Go module with the agreed path/version.
- Implement public types, error taxonomy, option validation, and ownership rules.
- Implement checked arithmetic, bounded reads, and minimal TIFF tag access.
- Add unit tests for byte orders, inline values, offsets, short reads, and
  32-bit allocation/index limits.
- Add a minimal README describing pure-Go constraints and milestone scope.

**Gate:** `CGO_ENABLED=0 go test ./...` and `go vet ./...` pass; no native
dependency enters the production graph; malformed primitive vectors return
typed errors without panics.

### Phase 2: ORF recognition, metadata, and capabilities

**Depends on:** phase 1.

**Tasks:**

- Implement accepted ORF signatures, bounded IFD traversal, raw IFD selection,
  EXIF, and the fixture-backed Olympus MakerNote variants.
- Resolve geometry, CFA, calibration presence, and orientation.
- Construct immutable decode plans and explicit unsupported classifications.
- Add `cmd/orfinfo` with deterministic JSON metadata/capability output.
- Implement separate bounded JPEG preview extraction.

**Gate:** manifest metadata matches reference expectations; cycles, ambiguous
raw IFDs, and incorrect bases are tested; previews match original embedded
bytes; metadata-only unsupported ORFs never claim RAW capability.

**Implementation status:** complete; see the Phase 2 section of
[README](README.md) for executable gates and the exact supported parsing
surface. Only evidenced `IIRO` framing and the old `OLYMP` / new little-endian
`OLYMPUS` MakerNotes are parsed. `IIRS` and `MMOR` report unsupported;
ordinary TIFF is not ORF. Layout IDs identify validated plans independently
of decoder availability; Phases 3-4 enable the two evidenced plans. JPEG selection
inspects bounded frame headers and
terminal markers without decoding entropy/pixels; extraction returns exact
embedded bytes. Unverified Olympus preview orientation remains unavailable.

### Phase 3: older word-based and packed layouts

**Depends on:** phase 2.

**Tasks:**

- Implement only the initial evidenced word/packed layout routines.
- Test byte/nibble order, row padding/interleave, legal shifts, and boundaries.
- Wire plans into `DecodeRaw`, including limits, cancellation, and ownership.
- Compare all required samples and geometry against pinned goldens.

**Gate:** exact parity for every required fixture in these layouts; synthetic
edge vectors pass; truncated/invalid payloads fail explicitly; unsupported
strip organizations are rejected rather than concatenated speculatively.

**Implementation status:** complete. The only initially evidenced word/packed
layout is `olympus-word16le-high12`; no unevidenced packed decoder was invented.
Every E-1/E-400 golden sensor sample matches (15,859,904 total). Word RAW
capabilities reflect implementation and working limits; Phase 4 adds predictive
decoding below. One output plane, one row and caller-owned diagnostics are
allocated, with cancellation/error/ownership and exact quota tests. See
[README](README.md) for executable gates, repeated review results, performance
baselines and the distinction between layout parity and camera certification.

### Phase 4: legacy predictive compressed ORF

**Depends on:** phase 3, reusing its complete raw-image/error contracts.

**Tasks:**

- Finalize the byte/bit-level legacy algorithm note from phase-0 evidence.
- Implement and test bit reader, bounded prefix/escape decoding, adaptive
  residual state, boundary/interior predictors, and sample reconstruction.
- Cover all predictor branches, state resets, signed shifts, and any coded
  columns not represented in output.
- Add first-mismatch diagnostics in the test/reference comparison tooling.

**Gate:** every required legacy compressed fixture has zero meaningful-sample
differences; malformed streams terminate; geometry and CFA match; missing
data cannot decode through zero-filled lookahead.

**Implementation status:** complete. Both E-410/E-510 fixtures match every
stored sensor sample (20,832,000 total), including margins. The frozen
[algorithm contract](docs/orf-layouts.md#phase-4-implementation-contract)
specifies low-16-bit adaptive width selection, signed arithmetic, bounded
prefixes/escapes, parity/row state, predictors, and opaque trailing-data policy.
The decoder uses one output plane plus a 4096-byte input buffer and diagnostics.
Tests cover predictor branches, all field truncations, ignored coded columns,
exact resource/capability boundaries, cancellation, ownership, concurrency,
and row-independent allocation counts. Real fixtures decode with exactly
exhausted strip extents and fail when their final required byte is removed,
even with the omitted bytes still physically readable. All 36,691,904 samples
across both layouts match the pinned oracles. See [README](README.md) for
validation commands, measured results, and remaining support boundaries.

### Phase 5: M1 hardening and raw-decode release

**Depends on:** phases 2-4.

**Tasks:**

- Fuzz container/MakerNote parsing and complete raw decoding with strict limits.
- Run race/concurrency tests and cancellation/resource-boundary cases.
- Measure parse/decode allocations and throughput; remove unnecessary full
  buffers without altering output.
- Document the tested camera/mode/layout matrix, API use, errors, and fixture
  workflow. Label M1 as raw decoding, not full color development.

**Gate:** all required fixtures pass; CGO-disabled tests/vet pass; race tests
pass where supported; at least 10 minutes per fuzz entry point completes
without a failure; Linux/macOS on amd64/arm64 build and applicable CI runners
test successfully; a 32-bit build and arithmetic-focused tests pass.
Use compile-only checks for unavailable target runners and state that
limitation explicitly. M1 can now be released.

### Phase 6: calibration and deterministic development

**Depends on:** phase 5.

**Tasks:**

- Add approved camera profiles only for evidenced camera/mode combinations.
- Implement black/white normalization, explicit WB, bilinear demosaic,
  verified color conversion, exposure, sRGB encoding, and orientation.
- Test stage equations independently before adding end-to-end goldens.
- Add explicit caller overrides and typed missing-calibration errors.

**Gate:** stage tests meet stated tolerances; four CFA origins, all eight
orientations, borders, and tiny images pass; no render option mutates RAW;
render support is advertised separately from decode support.

### Phase 7: M2 usable release

**Depends on:** phase 6.

**Tasks:**

- Add documented examples for metadata, preview, raw extraction, and PNG output.
- Finalize deterministic regression images and performance measurements.
- Validate the same API with filesystem readers and in-memory `ReaderAt`s.
- Publish limitations, calibration provenance, licenses, and support matrix.

**Gate:** the initial release criteria in section 11 pass. Do not claim all
Olympus or OM System cameras are supported.

### Phase 8: newer compression and additional modes (M3)

**Depends on:** phase 5; independent of rendering phases 6-7.

**Tasks:**

- Acquire newer 12/14-bit fixtures and pin an appropriate newer reference.
- Add a separate parameterized codec plan with strictly validated MakerNote
  parameters, headers, state, widths, and numeric-domain rules.
- Add one layout/mode at a time, each with exact differential tests.
- Evaluate high-resolution, ORI, or multi-frame support as separately approved
  scope; do not treat them as ordinary Bayer frames without proof.

**Gate per addition:** fixture-backed exact parity, resource/malformed tests,
calibration-domain checks, and explicit support-table update. Upgrading the
reference must not rewrite earlier goldens without an explained review.

## 11. Definition of done for the initial M1 + M2 release

- [ ] Project license, third-party notices, and fixture permissions are recorded.
- [ ] Production build and decode work with `CGO_ENABLED=0`.
- [ ] Advertised layouts and camera/modes have required fixtures.
- [ ] Raw sample comparisons have zero unexplained differences.
- [ ] Geometry, active area, CFA phase, numeric domain, and orientation agree
      with the documented reference contract.
- [ ] Unsupported/newer layouts return explicit unsupported outcomes.
- [ ] JPEG preview extraction is never presented as RAW decoding.
- [ ] Rendering works for explicitly calibrated supported cameras and fails
      clearly when required calibration is unavailable.
- [ ] Unit, integration, malformed-input, race, fuzz-smoke, and portability
      checks pass within their documented environments.
- [ ] Benchmarks demonstrate bounded allocations and document baseline speed.
- [ ] README/examples explain ownership, defaults, errors, supported cameras,
      deferred modes, fixture acquisition, and reference reproduction.
- [ ] No mandatory test silently skips and no ordinary test invokes LibRaw,
      ExifTool, or the network.

## 12. Reference starting points

These are research inputs, not permission to copy code/data. Mutable URLs
must be pinned to exact revisions in phase 0.

- [LibRaw data structures and raw/processed buffer distinctions](https://www.libraw.org/docs/API-datastruct-eng.html).
- [LibRaw 0.21.4 legacy decoder source](https://github.com/LibRaw/LibRaw/blob/9646d776c7c61976080a8f2be67928df0750493e/src/decoders/decoders_dcraw.cpp):
  the legacy Olympus loader is a useful behavioral reference.
- [Newer Olympus/OM System decoder](https://github.com/LibRaw/LibRaw/blob/a9a14b1970d188c25dade81f8326ff00a6e33649/src/decoders/olympus14.cpp):
  demonstrates the distinct parameterized 12/14-bit path.
- [Olympus metadata handling](https://github.com/LibRaw/LibRaw/blob/a9a14b1970d188c25dade81f8326ff00a6e33649/src/metadata/olympus.cpp)
  and [camera identification/geometry handling](https://github.com/LibRaw/LibRaw/blob/a9a14b1970d188c25dade81f8326ff00a6e33649/src/metadata/identify.cpp).
- [LibRaw TIFF parsing](https://github.com/LibRaw/LibRaw/blob/a9a14b1970d188c25dade81f8326ff00a6e33649/src/metadata/tiff.cpp).
- [ExifTool Olympus tag documentation](https://exiftool.org/TagNames/Olympus.html)
  and [Olympus metadata implementation](https://github.com/exiftool/exiftool/blob/master/lib/Image/ExifTool/Olympus.pm).
- [LibRaw LGPL terms](https://github.com/LibRaw/LibRaw/blob/a9a14b1970d188c25dade81f8326ff00a6e33649/LICENSE.LGPL)
  and [CDDL terms](https://github.com/LibRaw/LibRaw/blob/a9a14b1970d188c25dade81f8326ff00a6e33649/LICENSE.CDDL).
- [raw.pixls.us sample archive](https://raw.pixls.us/): verify each sample's
  individual license and attribution before use.
