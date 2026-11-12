// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"testing"

	"github.com/otuschhoff/libraw-go/internal/bounded"
	"github.com/otuschhoff/libraw-go/internal/tiff"
)

func profileTestReader() *tiff.Reader {
	source, _ := bounded.New(bytes.NewReader(nil), 0, 1)
	reader, _ := tiff.New(source, binary.LittleEndian, 4096, 16<<20)
	return reader
}

func approvedProfileParsers() []parser {
	var out []parser
	for i, model := range []string{"E-1", "E-400", "E-410", "E-510"} {
		p := parser{reader: profileTestReader(), cameraID: []string{"D4040", "S0009", "S0013", "S0010"}[i],
			mode: "SpecialMode=0 0 0;DriveMode=0 0 0"}
		if i == 0 {
			p.mode = "SpecialMode=0 0 0;DriveMode=0 0"
		} else if i == 2 {
			p.mode = "SpecialMode=2 1 0;DriveMode=1 1 0"
		}
		p.metadata.NormalizedMake, p.metadata.NormalizedModel = "Olympus", model
		out = append(out, p)
	}
	return out
}

func approvedProfilePlan(i int) decodePlan {
	return decodePlan{width: []int{2624, 3768, 3720, 3720}[i], height: []int{1966, 2840, 2800, 2800}[i],
		layout: []string{"olympus-word16le-high12", "olympus-word16le-high12", "olympus-legacy-predictive12", "olympus-legacy-predictive12"}[i], bits: 12}
}

func TestApprovedProfilesAgainstPinnedObservations(t *testing.T) {
	ids := []string{"olympus-e-1-1800", "olympus-e-400-2151", "olympus-e-410-1173", "olympus-e-510-5449"}
	for i, p := range approvedProfileParsers() {
		t.Run(ids[i], func(t *testing.T) {
			var oracle struct {
				Libraw struct {
					Maximum int
					RGB     [3][4]float64 `json:"rgb_cam"`
				}
			}
			data, err := os.ReadFile("testdata/reference/" + ids[i] + ".json")
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &oracle); err != nil {
				t.Fatal(err)
			}
			if err := p.applyProfile(approvedProfilePlan(i)); err != nil {
				t.Fatal(err)
			}
			cal := p.metadata.Calibration
			origin := OriginProfile
			if i < 2 {
				origin = OriginEstimate
			}
			if cal.White.Origin != origin || cal.Color.Origin != OriginProfile || cal.Color.Source == "" ||
				cal.Black.Present() || cal.WhiteBalance.Present() {
				t.Fatal("profile provenance or invented black/as-shot WB", cal)
			}
			for _, row := range cal.White.Value {
				for _, white := range row {
					closeFloat(t, white, float64(oracle.Libraw.Maximum))
				}
			}
			m, ok := renderColorMatrix(cal.Color.Value)
			if !ok {
				t.Fatal("approved profile unusable")
			}
			for y := range 3 {
				for x := range 3 {
					closeFloat(t, m[y][x], oracle.Libraw.RGB[y][x])
				}
			}
			neutral := transform3(matrix3(cal.Color.Value.CameraToXYZ), [3]float64{1, 1, 1})
			for c := range 3 {
				closeFloat(t, neutral[c], d65White()[c])
			}
		})
	}
}

func TestProfileSelectionAndPerFilePrecedence(t *testing.T) {
	for i, original := range approvedProfileParsers() {
		for _, change := range []func(*parser, *decodePlan){
			func(p *parser, plan *decodePlan) { p.metadata.NormalizedMake = "unknown" },
			func(p *parser, plan *decodePlan) { p.metadata.NormalizedModel += "x" },
			func(p *parser, plan *decodePlan) { p.cameraID += "x" },
			func(p *parser, plan *decodePlan) { p.mode += "x" },
			func(p *parser, plan *decodePlan) { plan.width++ },
			func(p *parser, plan *decodePlan) { plan.height++ },
			func(p *parser, plan *decodePlan) { plan.bits++ },
			func(p *parser, plan *decodePlan) { plan.layout = "unsupported" },
		} {
			p, plan := original, approvedProfilePlan(i)
			change(&p, &plan)
			if err := p.applyProfile(plan); err != nil || p.metadata.Calibration.White.Present() || p.metadata.Calibration.Color.Present() {
				t.Fatal("profile escaped its identity/mode/layout/geometry/depth scope", err)
			}
		}
		p := original
		p.metadata.Calibration = renderRaw(2, 2).Calibration
		before := p.metadata.Calibration
		if err := p.applyProfile(approvedProfilePlan(i)); err != nil || p.metadata.Calibration != before {
			t.Fatal("profile overrode per-file calibration", err)
		}
	}
	if _, ok := profileColor([9]int{}); ok {
		t.Fatal("singular profile accepted")
	}
}
