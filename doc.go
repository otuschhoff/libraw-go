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
// Metadata snapshots, raw images and successful previews are caller-owned.
// Concurrent operations require a concurrently readable input. Preview returns original
// embedded JPEG bytes, and ErrNoPreview when no candidate meets its limits.
package libraw
