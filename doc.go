// SPDX-License-Identifier: LGPL-2.1-only

// Package libraw defines the contracts for a pure-Go Olympus ORF reader.
//
// Phase 1 provides data types and validation, not ORF opening or decoding.
// Sensor samples are unprocessed codes, never an embedded preview or values
// scaled to fill uint16. Geometry is in unrotated sensor coordinates.
//
// Future file operations borrow an immutable io.ReaderAt and never close it.
// Metadata snapshots and successful image/preview results will be caller-owned;
// concurrent operations will require a concurrently readable input.
package libraw
