// SPDX-License-Identifier: LGPL-2.1-only
package libraw_test

import (
	"context"
	"errors"
	"image"
	"io"
	"reflect"
	"strings"
	"testing"

	libraw "github.com/otuschhoff/libraw-go"
)

func TestDefaultLimits(t *testing.T) {
	want := libraw.Limits{
		InputBytes: 1 << 30, Pixels: 100_000_000, Dimension: 32768,
		IFDs: 128, Depth: 16, EntriesPerIFD: 4096,
		MetadataBytes: 16 << 20, PreviewBytes: 64 << 20, WorkingBytes: 1 << 30,
	}
	got, err := (libraw.OpenOptions{}).Resolve()
	if err != nil || got != want || libraw.DefaultLimits() != want {
		t.Fatalf("defaults: %+v, %v; want %+v", got, err, want)
	}
	changed := libraw.DefaultLimits()
	changed.Pixels = 1
	if libraw.DefaultLimits().Pixels != 100_000_000 {
		t.Fatal("defaults expose mutable shared state")
	}
	partial, err := (libraw.OpenOptions{Limits: libraw.Limits{Pixels: 5}}).Resolve()
	if err != nil || partial.Pixels != 5 || partial.InputBytes != want.InputBytes {
		t.Fatalf("partial defaults: %+v, %v", partial, err)
	}
}

func TestLimitsRejectNegativesAndContradictions(t *testing.T) {
	typ := reflect.TypeFor[libraw.Limits]()
	for i := 0; i < typ.NumField(); i++ {
		t.Run(typ.Field(i).Name, func(t *testing.T) {
			limits := libraw.Limits{}
			reflect.ValueOf(&limits).Elem().Field(i).SetInt(-1)
			got, err := (libraw.OpenOptions{Limits: limits}).Resolve()
			if !errors.Is(err, libraw.ErrInvalidOptions) || got != (libraw.Limits{}) {
				t.Fatalf("negative limit: %+v, %v", got, err)
			}
		})
	}
	for _, limits := range []libraw.Limits{
		{Depth: 129}, {IFDs: 1},
		{WorkingBytes: 1},
		{MetadataBytes: 2 << 30}, {PreviewBytes: 2 << 30},
	} {
		if _, err := (libraw.OpenOptions{Limits: limits}).Resolve(); !errors.Is(err, libraw.ErrInvalidOptions) {
			t.Fatalf("contradictory limits %+v: %v", limits, err)
		}
	}
	raised := libraw.DefaultLimits()
	raised.InputBytes *= 2
	raised.Pixels *= 2
	raised.Dimension *= 2
	if got, err := (libraw.OpenOptions{Limits: raised}).Resolve(); err != nil || got != raised {
		t.Fatalf("positive raised limits not preserved: %+v, %v", got, err)
	}
}

func TestErrorClassificationAndContext(t *testing.T) {
	offset, ifd, tag, bit := int64(0), int64(8), uint16(0x100), uint8(3)
	for _, sentinel := range []error{
		libraw.ErrNotORF, libraw.ErrUnsupported, libraw.ErrCorrupt,
		libraw.ErrTruncated, libraw.ErrLimitExceeded,
		libraw.ErrCalibrationUnavailable, libraw.ErrInvalidOptions,
	} {
		for _, cause := range []error{io.ErrUnexpectedEOF, context.Canceled} {
			err := &libraw.Error{Op: "test", Kind: sentinel, Cause: cause,
				Offset: &offset, IFD: &ifd, Tag: &tag, Bit: &bit, Layout: "sample", Detail: "detail"}
			var typed *libraw.Error
			if !errors.Is(err, sentinel) || !errors.Is(err, cause) || !errors.As(err, &typed) {
				t.Fatalf("error does not preserve classification/cause: %v", err)
			}
			for _, text := range []string{"test", "offset=0", "IFD=8", "tag=0x0100", "bit=3", "sample", "detail"} {
				if !strings.Contains(err.Error(), text) {
					t.Fatalf("%q missing from %v", text, err)
				}
			}
		}
	}
	err := &libraw.Error{Op: "read", Cause: io.ErrClosedPipe}
	if !errors.Is(err, io.ErrClosedPipe) || strings.Contains(err.Error(), "offset=") {
		t.Fatalf("unknown location or I/O-only cause lost: %v", err)
	}
	if (&libraw.Error{Op: "empty"}).Error() != "empty" {
		t.Fatal("optional error fields should remain optional")
	}
}

func TestMetadataOwnershipAndPresence(t *testing.T) {
	original := libraw.Metadata{Make: "Original  ", Diagnostics: []libraw.Diagnostic{{Code: "notice", Message: "original"}},
		Geometry: libraw.Geometry{ActiveArea: libraw.Value[image.Rectangle]{
			Value: image.Rect(1, 2, 5, 6), Origin: libraw.OriginFile, Source: "tag"}},
	}
	clone := original.Clone()
	clone.Diagnostics[0].Message = "changed"
	clone.Geometry.ActiveArea.Value.Min.X = 10
	if original.Diagnostics[0].Message != "original" || original.Geometry.ActiveArea.Value.Min.X != 1 {
		t.Fatal("metadata snapshot aliases original")
	}
	if original.Make != "Original  " {
		t.Fatal("original make string changed")
	}
	if (libraw.Metadata{}).Clone().Diagnostics != nil {
		t.Fatal("nil diagnostic slice changed")
	}
	empty := libraw.Metadata{Diagnostics: []libraw.Diagnostic{}}.Clone()
	if empty.Diagnostics == nil {
		t.Fatal("empty diagnostic slice lost")
	}
	if (libraw.Value[float64]{}).Present() {
		t.Fatal("missing calibration marked present")
	}
	zero := libraw.Value[float64]{Value: 0, Origin: libraw.OriginFile, Source: "black"}
	if !zero.Present() {
		t.Fatal("valid zero calibration lost")
	}
}
