# Phase 0: evidence, scope, licensing, and completion review

Date: 2026-11-12.

Review status: **complete**. All Phase 0 gates passed after repeated
re-evaluation and correction of the findings below. No unresolved Phase 0
completion findings remain.
This document records Phase 0, not implementation of the production decoder.

## 1. Approved decisions

The project owner explicitly selected:

| Decision | Approved value |
|---|---|
| Module path | `github.com/otuschhoff/libraw-go` |
| Minimum production Go version | Go 1.26 |
| License | LGPL-2.1-only |
| Source strategy | Properly attributed, compliant LibRaw-derived implementation permitted |
| Clean-room claim | Not permitted; public implementation sources were inspected |

[LICENSE](../LICENSE), [COPYING.GPL](../COPYING.GPL), and
[THIRD_PARTY.md](../THIRD_PARTY.md) record the license and provenance decisions.
No production `go.mod`, parser, decoder, renderer, or public API was added;
those remain in their designated phases.

The exact Go 1.26.0 reference-tool pin is a reproducibility decision, not a
restriction preventing newer Go versions from building the future library.

## 2. Pinned reference inputs

| Input | Version | Pinned revision |
|---|---|---|
| LibRaw | 0.21.4-Release | `9646d776c7c61976080a8f2be67928df0750493e` |
| ExifTool | 13.40 | `e2a8542f9c900eae70cb016f0ed410c1bfd4c1ac` |
| Go golden generator | 1.26.0 | official `go1.26.0` toolchain |

Archive URLs and hashes, the independently compared LibRaw decoder-source
hash, and every native configure option are in the
[reference lock](../tools/reference/lock.json). The development exporter links
a newly built static library, not the preinstalled LibRaw version.
No dependency was installed globally.

The native build disables shared libraries, OpenMP, JPEG, Jasper, zlib, LCMS,
and example programs. The exporter fixes RawSpeed/DNG SDK use, options,
specials, shot selection, and a 512 MiB native raw allocation limit.
Metadata snapshots record those settings and source/lock hashes.

## 3. Initial variant/evidence matrix

**All production capabilities are currently unavailable.** The entries below
are reference-verified candidates for implementation, not advertised camera
support. There are two independent real files per layout and one per camera.

| Layout ID | Camera / ID | Original make | Raw dimensions / CFA at origin | Organization | Reference operation |
|---|---|---|---|---|---|
| `olympus-word16le-high12` | E-1 / D4040 | OLYMPUS CORPORATION | 2624x1966 / GRBG | 16 contiguous strips; 130 rows/strip | metadata + full RAW export |
| `olympus-word16le-high12` | E-400 / S0009 | OLYMPUS IMAGING CORP. | 3768x2840 / GRBG | 89 contiguous strips; 32 rows/strip | metadata + full RAW export |
| `olympus-legacy-predictive12` | E-410 / S0013 | OLYMPUS IMAGING CORP. | 3720x2800 / RGGB | one compressed strip | metadata + full RAW export |
| `olympus-legacy-predictive12` | E-510 / S0010 | OLYMPUS IMAGING CORP. | 3720x2800 / RGGB | one compressed strip | metadata + full RAW export |

All four have IIRO little-endian containers, TIFF BitsPerSample 16,
Compression 1, ValidBits 12, and orientation 1. The manifest binds each row
to the full input SHA-256, source URL/license, native decoder name, and mode
observations. The per-fixture JSON preserves original make strings and
normalized native make/model separately.

Firmware is independently unknown. Software tags are retained rather than
misrepresented as verified firmware IDs. The E-410 contains burst/panorama
sequence metadata but only one stored Bayer frame. See the
[layout notes](orf-layouts.md) for precise framing, mode restrictions,
MakerNote bases, crop rectangles, WB/black data, and primitive branch vectors.

Reference preview extraction and color rendering are not implemented or
certified by Phase 0. Native/exif preview-location metadata is evidence only.

## 4. Corpus and oracle outputs

Original inputs were acquired from raw.pixls.us and verified against its
published hashes. Each selected file is individually CC0-1.0; the
[saved source rows](../testdata/provenance/raw-pixls-selected.json) retain the
per-file dedication and hash, rather than assuming a site-wide license.
No noncommercial/otherwise restricted archive sample was included.

Original ORFs live in ignored `.cache/fixtures/` and are reproducible through
the explicit hash-checking `fetch` command. The tracked reference corpus
contains 36,691,904 sensor samples (73,383,808 uncompressed bytes) in four
full-frame gzip streams, plus JSON metadata/layout observations and an
[artifact index](../testdata/reference/index.json).

Uncompressed sensor-stream hashes:

| Fixture | Samples | SHA-256 |
|---|---:|---|
| E-1 / 1800 | 5158784 | `48a755038aa94760a34f2e6dcededd882224a332ce6de5bd813b0b74fa7e72f3` |
| E-400 / 2151 | 10701120 | `2e675bd36bcfbe5de7d9afa1cc4fc8cd2aeb74d9f96f38ca30010918031fe2e3` |
| E-410 / 1173 | 10416000 | `6fd3d64305d4be04759d9ba0599a7ae1d8b07a287171c4f58df837580243f3fd` |
| E-510 / 5449 | 10416000 | `04bc6a9ba70a6c3dd6be307c593ed95384db0f42cee882bf55768038b6863d55` |

The word-layout measurement independently checks every stored word shifted
right by four against the native sensor stream: all 15,859,904 samples match.
The compressed fixtures establish a trusted exact oracle; no independent
production compressed decoder exists yet.

Every native export excludes uninitialized allocation padding and reports
its region, stride, raw/decoded geometry, CFA, black/white values, WB, matrices,
orientation, masks, data errors, and warnings. ExifTool independently records
selected EXIF/MakerNote values and all validation warnings with unique
instance-group names.

## 5. Re-evaluation findings and resolutions

| Finding | Resolution |
|---|---|
| Incomplete raw.pixls.us download URLs return a tiny HTTP 200 error body | Use full archive URLs and require the published content hash before publication |
| Pinned GitHub archive contains a global PAX metadata header | Ignore that non-file record; continue rejecting traversal, links, and unexpected roots |
| ExifTool numeric version `13.40` loses its trailing zero when decoded as float | Preserve JSON numbers during canonicalization |
| Ordinary ExifTool extraction omits verbose-only short-directory warnings | Enable ExifTool validation and retain its diagnostics |
| Duplicate tag names can be lost when JSON objects are decoded into maps | Request family-4 instance grouping; test duplicate preservation |
| Go 1.26 and 1.27 generate different gzip bytes from the same raw streams | Pin golden generation to Go 1.26.0 and fail explicitly on another generator version |
| E-400 has 793 nonzero low storage nibbles | Record and test the actual right-shift conversion; do not require zero discarded bits |
| Older OLYMP MakerNote subdirectories declare short byte lengths | Document the bounded compatibility rule and retain warnings; do not invent padded values |
| LibRaw's inset crop is not its raw-buffer margin | Record both and preserve full uncropped sensor samples |
| Sample archive's "16bit" and TIFF compression fields are misleading codec selectors | Record ValidBits, payload layout/prefix, camera/mode identity, and reference decoder separately |
| Some camera calibration comes from native reference tables | Mark it reference-derived, preserve attribution, and defer production profile semantics to Phase 6 |

The first source/golden-generation attempts were intentionally not treated as
completion. Each observed tooling failure was corrected, followed by unit
checks and fresh reference generation. Any generated metadata changes during
this phase came from the pinned native oracle, never from a proposed Go decoder.

## 6. Final validation record

All checks below passed on the final implementation:

| Check | Command / method | Result |
|---|---|---|
| Approved minimum Go | `GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go test tools/reference/main.go tools/reference/main_test.go` | PASS, no skipped tests |
| Static checks | `GOTOOLCHAIN=go1.26.0 CGO_ENABLED=0 go vet tools/reference/main.go tools/reference/main_test.go` | PASS |
| Newer Go | Same unit tests with local Go 1.27.3 and `CGO_ENABLED=0` | PASS |
| Race checks | `GOTOOLCHAIN=go1.26.0 CGO_ENABLED=1 go test -race tools/reference/main.go tools/reference/main_test.go` | PASS |
| 32-bit portability | `GOTOOLCHAIN=go1.26.0 GOOS=linux GOARCH=386 CGO_ENABLED=0 go test -c` with the same source/test file list | PASS, compile-only; not executed on this host |
| Uncached acquisition | Run pinned `fetch` with both source archives and all four ORFs absent from the cache | All six downloaded by the Go tool and matched their pinned hashes |
| Pinned native build | `build` extracts fresh hash-verified sources and builds locally | PASS |
| Artifact integrity | Pinned `verify` command | PASS for all four required fixtures, artifact hashes/lengths, decompressed streams, and measured layouts |
| Repeated oracle exports | Pinned `reproduce` command | Two fresh complete export passes, every artifact byte-identical to the reviewed index |
| Storage conversion | Independent word comparison during each generation | Zero differences across 15,859,904 word-layout samples |
| Oracle stability | Compare final uncompressed sensor hashes to the first successful exports before review fixes | All four unchanged |
| Native usage errors | Call exporter without arguments | Exit 2, explicit usage, no successful output |
| Native metadata truncation | 1024-byte prefix of the E-410 input | Exit 1 from `open_file`, no sample output |
| Native payload truncation | 656000-byte prefix of the E-410 input, preserving metadata and only 640 payload bytes | Exit 1 from `unpack`, no sample output |

Offline unit tests include successful/corrupt cached downloads, a fake HTTP
200 error body, HTTP failure, deterministic compression, malformed IFDs,
word conversion/mismatch detection, license-evidence contradictions,
reference-toolchain enforcement, duplicate-safe metadata/diagnostics,
trailing JSON rejection, command validation, path/link rejection, and the
global-PAX regression case.

Reference environment: macOS/arm64; Apple clang 21.0.0
`clang-2100.1.1.101`; available Go 1.27.3 and pinned Go 1.26.0; ExifTool runs
under the system Perl. This is not a claim that later cross-platform decoder
or fuzz-hardening gates have already been executed.

## 7. Phase boundary and outstanding later-phase work

Phase 0's gate requires approved decisions, legal hashed fixtures,
evidence for at least one compressed and one older word/packed layout,
layout notes, a built oracle exporter, and repeatable exports.
Every item is complete and verified by section 6:

- [x] Module path, Go floor, and license strategy confirmed by the owner.
- [x] Reference versions, source/archive hashes, and build/unpack settings pinned.
- [x] Four legal fixtures acquired and verified against per-file source evidence.
- [x] Headers, IFD organization, MakerNote bases/quirks, layouts, and modes inspected.
- [x] Variant/evidence matrix, geometry/calibration notes, and branch vectors written.
- [x] Development-only exporter built and full sensor/metadata goldens produced.
- [x] Two fresh complete export passes reproduce every artifact byte-for-byte.
- [x] Re-evaluation findings resolved without reducing M1 scope or claiming
      nonexistent production support.

The following are intentionally **not Phase 0 completion gaps**:

- Phase 1 initializes the agreed module and production API/binary primitives.
- Phases 2-4 implement/test production parsing and the evidenced decoders,
  including bounded old-MakerNote handling and bitstream tail policy.
- Additional photographs are required before advertising any camera/mode:
  the two-per-advertised-camera design criterion has not been waived.
- Big-endian/alternate signatures, packed formats, compressed multi-strip
  restart rules, and newer parameterized 12/14-bit codecs lack initial evidence.
- High-resolution, pixel-shift, ORI, and multi-frame reconstruction are deferred.
- Production calibration profiles, color development, preview support,
  production fuzzing, and production cross-platform/runtime behavior remain
  their designated later phases.

No M1 scope reduction is requested: Phase 0 contains both required layout
families. The support matrix remains honest about missing production code and
future certification requirements.
