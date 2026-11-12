// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestEM5WhiteBalanceEncoding(t *testing.T) {
	for _, test := range []struct {
		name      string
		model, id string
		values    []uint32
		ok        bool
	}{
		{"four", "E-M5", "S0036", []uint32{384, 512, 256, 256}, true},
		{"two", "E-M5", "S0036", []uint32{384, 512}, true},
		{"green1", "E-M5", "S0036", []uint32{384, 512, 255, 256}, false},
		{"green2", "E-M5", "S0036", []uint32{384, 512, 256, 255}, false},
		{"count", "E-M5", "S0036", []uint32{384, 512, 256}, false},
		{"model", "E-M5 Mark II", "S0036", []uint32{384, 512, 256, 256}, false},
		{"identity", "E-M5", "unknown", []uint32{384, 512, 256, 256}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := em5TestORF(test.model, test.id)
			entry := recordAt(data, 1800, 0x100)
			binary.LittleEndian.PutUint32(entry[4:], uint32(len(test.values)))
			binary.LittleEndian.PutUint32(entry[8:], 7400-1024)
			if len(test.values) <= 2 {
				for i, v := range test.values {
					binary.LittleEndian.PutUint16(entry[8+i*2:], uint16(v))
				}
			} else {
				for i, v := range test.values {
					binary.LittleEndian.PutUint16(data[7400+i*2:], uint16(v))
				}
			}
			file, err := Open(bytes.NewReader(data), int64(len(data)), OpenOptions{})
			if (err == nil) != test.ok {
				t.Fatalf("WB acceptance=%v: %v", test.ok, err)
			}
			if err != nil && !errors.Is(err, ErrCorrupt) {
				t.Fatal(err)
			}
			if test.ok && file.Metadata().Calibration.WhiteBalance.Value != (Levels{{1.5, 1}, {1, 2}}) {
				t.Fatal("wrong WB gains")
			}
		})
	}
}

func TestEM5ModesAndProfileScope(t *testing.T) {
	modes := []string{"SpecialMode=0 0 0;DriveMode=0 0 0",
		"SpecialMode=2 1 0;DriveMode=5 3 1", "SpecialMode=2 1 0;DriveMode=5 4 1",
		"SpecialMode=2 1 0;DriveMode=5 5 1", "SpecialMode=2 2 0;DriveMode=5 4 1",
		"SpecialMode=2 2 0;DriveMode=5 5 1", "SpecialMode=2 3 0;DriveMode=5 5 1"}
	for i := 1; i <= 14; i++ {
		modes = append(modes, fmt.Sprintf("SpecialMode=2 %d 0;DriveMode=1 %d 0", i, i))
	}
	for i := 1; i <= 5; i++ {
		modes = append(modes, fmt.Sprintf("SpecialMode=2 %d 0;DriveMode=5 %d 1", i, i))
	}
	for _, mode := range modes {
		p := parser{cameraID: "S0036", mode: mode}
		p.metadata.NormalizedMake, p.metadata.NormalizedModel = "Olympus", "E-M5"
		plan := decodePlan{width: 4640, height: 3472, bits: 12, layout: "olympus-legacy-predictive12"}
		if !em5CaptureMode(mode) {
			t.Fatal("observed mode rejected", mode)
		}
		if err := p.applyProfile(plan); err != nil {
			t.Fatal(err)
		}
		cal := p.metadata.Calibration
		if cal.White.Origin != OriginProfile || cal.White.Value[0][0] != 4065 || cal.Color.Origin != OriginProfile ||
			cal.Black.Present() || cal.WhiteBalance.Present() {
			t.Fatal("incorrect/invented calibration")
		}
		rgb, ok := renderColorMatrix(cal.Color.Value)
		if !ok {
			t.Fatal("invalid color profile")
		}
		// Independent pinned LibRaw 0.21.4 rgb_cam observation, not image data.
		want := [3][3]float64{{1.7054885625839233, -.5323868989944458, -.17310167849063873},
			{-.2921513020992279, 1.816804051399231, -.5246527791023254},
			{.03221755102276802, -.4933459460735321, 1.4611283540725708}}
		for i := range 3 {
			for j := range 3 {
				closeFloat(t, rgb[i][j], want[i][j])
			}
		}
		for _, change := range []func(*parser, *decodePlan){
			func(p *parser, d *decodePlan) { p.cameraID = "other" },
			func(p *parser, d *decodePlan) { p.metadata.NormalizedModel = "E-M5 Mark II" },
			func(p *parser, d *decodePlan) { p.metadata.NormalizedMake = "unknown" },
			func(p *parser, d *decodePlan) { p.mode += " " },
			func(p *parser, d *decodePlan) { d.width++ },
			func(p *parser, d *decodePlan) { d.height++ },
			func(p *parser, d *decodePlan) { d.bits = 14 },
			func(p *parser, d *decodePlan) { d.layout = "new-codec" },
		} {
			q, d := p, plan
			q.metadata.Calibration = Calibration{}
			change(&q, &d)
			if err := q.applyProfile(d); err != nil || q.metadata.Calibration.White.Present() || q.metadata.Calibration.Color.Present() {
				t.Fatal("profile escaped scope", err)
			}
		}
	}
	for _, mode := range []string{"", "SpecialMode=2 15 0;DriveMode=1 15 0", "SpecialMode=2 0 0;DriveMode=1 0 0",
		"SpecialMode=2 1 0;DriveMode=5 1 0", "SpecialMode=2 6 0;DriveMode=5 6 1",
		"SpecialMode=2 1 0;DriveMode=1 2 0"} {
		if em5CaptureMode(mode) {
			t.Fatal("unevidenced mode accepted", mode)
		}
	}
}

func TestEM5GeometryRequiredForDecoder(t *testing.T) {
	data := em5TestORF("E-M5", "S0036")
	file, err := Open(bytes.NewReader(data), int64(len(data)), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if file.Capabilities().Raw.Available || file.Metadata().Calibration.Color.Present() {
		t.Fatal("tiny synthetic geometry acquired real-camera support")
	}
}

func TestEM5FullGeometryLayoutAndCapabilities(t *testing.T) {
	for _, mode := range []string{
		"SpecialMode=0 0 0;DriveMode=0 0 0",
		"SpecialMode=2 14 0;DriveMode=1 14 0",
		"SpecialMode=2 1 0;DriveMode=5 3 1",
	} {
		data := em5TestORF("E-M5", "S0036")
		for tag, value := range map[uint16]uint32{0x100: 4640, 0x101: 3472, 0x116: 3472} {
			binary.LittleEndian.PutUint32(recordAt(data, 8, tag)[8:], value)
		}
		parts := strings.Split(mode, ";")
		var special, drive [3]uint32
		if _, err := fmt.Sscanf(parts[0], "SpecialMode=%d %d %d", &special[0], &special[1], &special[2]); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Sscanf(parts[1], "DriveMode=%d %d %d", &drive[0], &drive[1], &drive[2]); err != nil {
			t.Fatal(err)
		}
		for _, field := range []struct {
			at     int
			tag    uint16
			values [3]uint32
			short  bool
		}{{1036, 0x0200, special, false}, {1500, 0x0600, drive, true}} {
			offset := int(binary.LittleEndian.Uint32(recordAt(data, field.at, field.tag)[8:])) + 1024
			for i, value := range field.values {
				if field.short {
					binary.LittleEndian.PutUint16(data[offset+i*2:], uint16(value))
				} else {
					binary.LittleEndian.PutUint32(data[offset+i*4:], value)
				}
			}
		}
		file := openTest(t, data, OpenOptions{})
		if !file.Capabilities().Raw.Available || !file.Capabilities().Render.Available ||
			file.Metadata().Layout.Value != "olympus-legacy-predictive12" || file.Metadata().CaptureMode.Value != mode {
			t.Fatal("E-M5 selector/profile/capability wiring incomplete", file.Capabilities())
		}
		binary.LittleEndian.PutUint32(recordAt(data, 8, 0x100)[8:], 4642)
		file = openTest(t, data, OpenOptions{})
		if file.Capabilities().Raw.Available || file.Capabilities().Render.Available {
			t.Fatal("unevidenced geometry acquired decoding")
		}
	}
}

func em5TestORF(model, id string) []byte {
	data := makeTestORF(true)
	for _, field := range []struct {
		at, offset, base int
		tag              uint16
		text             string
	}{
		{8, 7000, 0, 0x110, model}, {1400, 7200, 1024, 0x100, id},
	} {
		entry := recordAt(data, field.at, field.tag)
		binary.LittleEndian.PutUint32(entry[4:], uint32(len(field.text)+1))
		binary.LittleEndian.PutUint32(entry[8:], uint32(field.offset-field.base))
		copy(data[field.offset:], field.text+"\x00")
	}
	return data
}

func FuzzEM5Metadata(f *testing.F) {
	f.Add(uint16(384), uint16(512), uint16(256), uint16(256), uint8(4))
	f.Add(uint16(0), uint16(0), uint16(0), uint16(0), uint8(3))
	f.Fuzz(func(t *testing.T, red, blue, green1, green2 uint16, count uint8) {
		data := em5TestORF("E-M5", "S0036")
		entry := recordAt(data, 1800, 0x0100)
		binary.LittleEndian.PutUint32(entry[4:], uint32(count))
		binary.LittleEndian.PutUint32(entry[8:], 7400-1024)
		for i, value := range []uint16{red, blue, green1, green2} {
			binary.LittleEndian.PutUint16(data[7400+i*2:], value)
		}
		file, err := Open(bytes.NewReader(data), int64(len(data)), OpenOptions{
			Limits: Limits{InputBytes: 32768, Pixels: 1024, Dimension: 64, MetadataBytes: 32768,
				PreviewBytes: 1, WorkingBytes: 65536, IFDs: 16, Depth: 8, EntriesPerIFD: 64},
		})
		if err != nil {
			if file != nil {
				t.Fatal("partial file on malformed E-M5 metadata")
			}
			return
		}
		checkFuzzOperations(t, file)
	})
}
