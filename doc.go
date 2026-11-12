// SPDX-License-Identifier: LGPL-2.1-only

// Package libraw provides bounded, pure-Go Olympus ORF metadata, previews,
// evidenced word-layout and legacy predictive raw sensor decoding, and basic
// calibrated Bayer development to opaque sRGB NRGBA64.
//
// The active target is E-M5/S0036, 4640x3472, 12-bit legacy predictive files
// with evidenced normal/sequence/bracket metadata. Historical camera paths
// remain compatibility behavior, not a claim of universal ORF support.
//
// Phases 3-4 decode olympus-word16le-high12 and olympus-legacy-predictive12
// using validated IIRO and Olympus MakerNote evidence. Other raw layouts return
// ErrUnsupported; an identified layout alone does not imply decoding capability.
// Render independently requires verified calibration/crop or explicit overrides.
// Approved fixture-mode profiles supplement file calibration; bit-depth white
// estimates require explicit consent. This is not universal camera support.
// Sensor samples are unprocessed codes, never an embedded preview or values
// scaled to fill uint16. Geometry is in unrotated sensor coordinates.
//
// File operations borrow an immutable io.ReaderAt and never close it.
// Open, Preview, DecodeRaw and Render accept context.Context and cooperate with
// cancellation during parsing, reads and pixel loops. A blocked ReaderAt call
// and Go allocations cannot be interrupted. Errors preserve context errors.
//
// File, RawImage, Preview and RenderedImage must be closed to release shared
// Budget reservations. Limits remain per-operation quotas; outputs count until
// Close, scratch until return, and shared immutable metadata until its final
// dependent handle closes. Metadata returns borrowed read-only diagnostics;
// Clone explicitly creates an unbudgeted caller-owned snapshot. Budgets cover
// explicit buffers and conservative parser allowances, not heap/RSS, caller
// memory, GC retention or all dependency allocations. They do not replace
// subprocess isolation against fatal runtime failures or memory exhaustion.
//
// Concurrent operations require a concurrently readable input and share their
// inherited Budget. Closing or mutating a handle must not overlap its use.
// Preview returns original
// embedded JPEG bytes, and ErrNoPreview when no candidate meets its limits.
package libraw
