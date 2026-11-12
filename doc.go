// SPDX-License-Identifier: LGPL-2.1-only

// Package libraw provides bounded, pure-Go Olympus ORF metadata, previews,
// and evidenced word-layout raw sensor decoding.
//
// Phase 3 decodes olympus-word16le-high12 using validated IIRO and Olympus
// MakerNote evidence. Other raw layouts return ErrUnsupported; an identified
// layout alone does not imply decoding capability. Rendering is not implemented.
// Sensor samples are unprocessed codes, never an embedded preview or values
// scaled to fill uint16. Geometry is in unrotated sensor coordinates.
//
// File operations borrow an immutable io.ReaderAt and never close it.
// Metadata snapshots, raw images and successful previews are caller-owned.
// Concurrent operations require a concurrently readable input. Preview returns original
// embedded JPEG bytes, and ErrNoPreview when no candidate meets its limits.
package libraw
