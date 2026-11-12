// SPDX-License-Identifier: LGPL-2.1-only

// Package libraw provides bounded, pure-Go Olympus ORF metadata and previews.
//
// Phase 2 recognizes the evidenced IIRO container and two Olympus MakerNote
// variants. Identified raw layouts do not imply an implemented pixel decoder:
// DecodeRaw currently returns ErrUnsupported. Rendering is not implemented.
// Sensor samples are unprocessed codes, never an embedded preview or values
// scaled to fill uint16. Geometry is in unrotated sensor coordinates.
//
// File operations borrow an immutable io.ReaderAt and never close it.
// Metadata snapshots and successful previews are caller-owned; concurrent
// operations require a concurrently readable input. Preview returns original
// embedded JPEG bytes, and ErrNoPreview when no candidate meets its limits.
package libraw
