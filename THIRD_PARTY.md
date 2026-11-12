# Third-party provenance and licensing

## Project license decision

The owner explicitly approved LGPL-2.1-only and license-compliant
LibRaw-derived implementation during Phase 0. The project's license text is
in [LICENSE](LICENSE); [COPYING.GPL](COPYING.GPL) supplies the GPL version 2
terms referenced by the LGPL. SPDX identifier: `LGPL-2.1-only`.

The LGPL text was obtained verbatim from the pinned LibRaw distribution.
The GPL text was obtained verbatim from
<https://www.gnu.org/licenses/old-licenses/gpl-2.0.txt>.
The LGPL file retains an upstream whitespace-only line to preserve its exact
bytes; authored-file whitespace checks exclude that verbatim license file.
Their SHA-256 values are respectively:

```text
eea173a556abac0370461e57e12aab266894ea6be3874c2be05fd87871f75449  LICENSE
edaef632cbb643e4e7a221717a6c441a4c1a7c918e6e4d56debc3d8739b233f6  COPYING.GPL
```

This records a project licensing choice, not a claim that future
distributions automatically satisfy every applicable obligation.

## LibRaw

- Upstream: <https://github.com/LibRaw/LibRaw>.
- Reference release: 0.21.4.
- Commit: `9646d776c7c61976080a8f2be67928df0750493e`.
- Distribution/archive hash and configure options:
  [reference lock](tools/reference/lock.json).
- Upstream offers LGPL version 2.1 or CDDL version 1.0; this project elects
  the LGPL version 2.1 route for any LibRaw-derived material.
- LibRaw is copyright LibRaw LLC. Its source notices also credit Dave
  Coffin's dcraw, copyright 1997-2018 Dave Coffin.

LibRaw source and compiled libraries are downloaded/built only into ignored
local cache storage. They are not vendored, linked into production Go code,
or distributed as binaries in this commit.

The measured algorithm notes are informed by LibRaw. Exported JSON contains
LibRaw-derived calibration values as reference observations; these are not
new independently authored/permissively licensed camera tables. Those
observations and the project tooling are distributed under the project LGPL
terms. Native LibRaw also contains third-party components with their own
notices; consult its pinned `COPYRIGHT`, `LICENSE.LGPL`, and source-file
headers before distributing a compiled reference binary or reusing code.
Phase 0 does not approve indiscriminate copying of unrelated components.

The exporter itself is project-authored glue around LibRaw's API. It does
not contain a copied native decoder.

The source-informed Go predictive decoder in [legacy.go](legacy.go) adapts
the algorithm of `olympus_load_raw` and `getbithuff` in the pinned
`src/decoders/decoders_dcraw.cpp`, whose notices identify copyright 2019-2021
LibRaw LLC and copyright 1997-2018 Dave Coffin. This adaptation is distributed
under the project's elected LGPL-2.1-only terms. Its bounded streaming reader,
explicit signed arithmetic, validation, cancellation, and Go integration are
documented in the [algorithm contract](docs/orf-layouts.md).
No native decoder code, library, process, or generated Huffman table is needed
at production runtime.

### Phase 6 camera profiles and color conventions

On 2026-11-12 the owner explicitly approved LibRaw-derived profiles only for
the four evidenced camera/identity/mode/layout/geometry combinations.
[profiles.go](profiles.go) uses the E-1/E-400/E-410/E-510 coefficients from the
pinned `src/tables/colordata.cpp`, copyright 2019-2021 LibRaw LLC with the
distribution's Dave Coffin attribution. The elected LGPL-2.1-only terms apply.
The XYZ-to-camera direction, D65 normalization and inversion are verified
against `cam_xyz_coeff` in `src/utils/utils_dcraw.cpp` and the exported
reference observations, not guessed from a MakerNote matrix.

[color.go](color.go) uses explicit standard sRGB/XYZ and Bradford conventions.
The [rendering report](docs/rendering.md) records matrix equations, exact
source-file hashes, provenance and tests. The E-410/E-510 saturation 3946 comes
from the profile table. E-1/E-400's 4095 is only the bit-depth reference default,
marked as an estimate requiring caller permission. No per-file black/WB
observations are promoted into generic camera tables, and no camera
certification, native runtime dependency or illuminant interpolation is implied.

### E-M5-only scope extension

The owner subsequently requested full implementation of the personal E-M5
ORF type. The narrowly scoped S0036 profile uses the E-M5 (not Mark II/III)
entry in the same pinned LibRaw `colordata.cpp`, including maximum `0xfe1`
and its nine color coefficients, under the same LGPL-2.1-only election and
upstream attribution. The [E-M5 report](docs/em5.md) records the source hashes,
WB interpretation and native validation workflow. No personal black/WB
observations or image samples are promoted into generic calibration tables.
Private photographs and their derived images are not included in this change;
the existing CC0 manifest is not extended by assumption.

## ExifTool

- Upstream: <https://github.com/exiftool/exiftool>.
- Version 13.40, commit `e2a8542f9c900eae70cb016f0ed410c1bfd4c1ac`.
- Copyright 2003-2025 Phil Harvey, as stated by the pinned README.
- Upstream licenses it under the same terms as Perl, either the Perl Artistic
  License or GPL.

ExifTool is downloaded and invoked only for development. Its program source
is not vendored. The reference JSON includes selected factual tag
observations, not the ExifTool tag database. Do not copy its implementation
or tables under an assumed LGPL license.

## Photographs and sample-plane goldens

The four original photographs were individually marked CC0-1.0 by
raw.pixls.us. The [saved source rows](testdata/provenance/raw-pixls-selected.json)
retain each exact license link, download URL, model, and published SHA-256.
The [fixture manifest](testdata/fixtures.json) binds those rows to local IDs.
CC0 text and terms: <https://creativecommons.org/publicdomain/zero/1.0/>.

Original ORFs are opt-in downloads into ignored cache storage. The exported
sensor samples are derived from those CC0 photographs and remain CC0; do not
mistake the project's software license for a restriction on the photographs.
The sample files do not contain a newly authored camera calibration table.

Phase 7's full-resolution [rendered PNGs](testdata/render/) are also derived
from these same four approved CC0 photographs and remain CC0-1.0. They are
project-renderer regression outputs, not new independent photographs or a
LibRaw color oracle. Their [index](testdata/render/index.json) records separate
image and metadata licenses: metadata/calibration observations and project
tooling remain LGPL-2.1-only, including the LibRaw-derived profile data.
Source-image/sample hashes and saved per-file CC0 evidence are bound in that
index; it does not relicense camera coefficients as CC0 or enlarge the approved
photo corpus. Reproduction and release restrictions are documented in the
[readiness report](docs/release-readiness.md).

Only the selected CC0 files are approved for this corpus. Other photographs
in the same public archive may have different, restrictive licenses.
The archive's human-readable "16bit" labels describe neither a verified
sensor depth nor an uncompressed-layout guarantee.

No third-party source or photograph is sent to an external analysis service
by the tooling. Downloads contact only the configured reference/sample hosts;
tests use local in-process HTTP servers.
