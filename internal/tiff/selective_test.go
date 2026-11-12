// SPDX-License-Identifier: LGPL-2.1-only

package tiff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/otuschhoff/libraw-go/internal/bounded"
	"github.com/otuschhoff/libraw-go/internal/fault"
)

func TestSelectiveAndExplicitDirectoryFraming(t *testing.T) {
	data := make([]byte, 30)
	binary.LittleEndian.PutUint16(data[8:], 1)
	binary.LittleEndian.PutUint16(data[10:], 0x100)
	binary.LittleEndian.PutUint16(data[12:], uint16(Undefined))
	binary.LittleEndian.PutUint32(data[14:], 8)
	binary.LittleEndian.PutUint32(data[18:], 22)
	source, err := bounded.New(bytes.NewReader(data), int64(len(data)), 1024)
	if err != nil {
		t.Fatal(err)
	}
	newReader := func(budget uint64) *Reader {
		t.Helper()
		reader, err := New(source, binary.LittleEndian, 8, budget)
		if err != nil {
			t.Fatal(err)
		}
		return reader
	}
	reader := newReader(204)
	ifd, err := reader.ReadDirectory(8, 0, false, func(uint16) bool { return false })
	if err != nil || len(ifd.Entries) != 1 || ifd.Entries[0].Data != nil || ifd.Next != 0 || reader.Remaining() != 0 {
		t.Fatalf("selective frame/budget: %+v, %v", ifd, err)
	}
	if _, err := ifd.Entries[0].Uint(0); !errors.Is(err, fault.Corrupt) {
		t.Fatal("unmaterialized entry accessed")
	}
	if _, err := newReader(204).ReadDirectory(8, 0, false, nil); !errors.Is(err, fault.Limit) {
		t.Fatal("selected value not charged")
	}
	// Skipping a value must not waive its extent validation.
	binary.LittleEndian.PutUint32(data[18:], 23)
	if _, err := newReader(512).ReadDirectory(8, 0, false, func(uint16) bool { return false }); !errors.Is(err, fault.Truncated) {
		t.Fatalf("unselected extent not checked: %v", err)
	}
	short := data[:22]
	shortSource, _ := bounded.New(bytes.NewReader(short), int64(len(short)), 1024)
	shortReader, _ := New(shortSource, binary.LittleEndian, 8, 1024)
	binary.LittleEndian.PutUint16(short[12:], uint16(Short))
	binary.LittleEndian.PutUint32(short[14:], 1)
	if _, err := shortReader.ReadDirectory(8, 0, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := shortReader.ReadIFD(8, 0); !errors.Is(err, fault.Truncated) {
		t.Fatal("classic framing relaxed")
	}
}

func TestSharedBudgetAcrossByteOrders(t *testing.T) {
	source, _ := bounded.New(bytes.NewReader(make([]byte, 8)), 8, 8)
	reader, _ := New(source, binary.LittleEndian, 8, 10)
	other, err := reader.WithOrder(binary.BigEndian)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Reserve(6); err != nil || other.Remaining() != 4 {
		t.Fatal("budget not shared")
	}
	if err := other.Reserve(5); !errors.Is(err, fault.Limit) || reader.Remaining() != 4 {
		t.Fatal("endian domain reset quota")
	}
	if _, err := reader.WithOrder(nil); !errors.Is(err, fault.Options) {
		t.Fatal("invalid byte order accepted")
	}
}
