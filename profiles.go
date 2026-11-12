// SPDX-License-Identifier: LGPL-2.1-only

package libraw

// Coefficients derive from LibRaw 0.21.4 colordata.cpp, copyright
// 2019-2021 LibRaw LLC and 1997-2018 Dave Coffin; see THIRD_PARTY.md.
func (p *parser) applyProfile(plan decodePlan) error {
	m := &p.metadata
	var coefficients [9]int
	width, height, white := 0, 0, 0
	origin := OriginProfile
	if m.NormalizedMake == "Olympus" && plan.bits == 12 {
		switch {
		case m.NormalizedModel == "E-1" && p.cameraID == "D4040" && plan.layout == "olympus-word16le-high12" &&
			p.mode == "SpecialMode=0 0 0;DriveMode=0 0":
			width, height, white, origin = 2624, 1966, 4095, OriginEstimate
			coefficients = [9]int{11846, -4767, -945, -7027, 15878, 1089, -2699, 4122, 8311}
		case m.NormalizedModel == "E-400" && p.cameraID == "S0009" && plan.layout == "olympus-word16le-high12" &&
			p.mode == "SpecialMode=0 0 0;DriveMode=0 0 0":
			width, height, white, origin = 3768, 2840, 4095, OriginEstimate
			coefficients = [9]int{6169, -1483, -21, -7107, 14761, 2536, -2904, 3580, 8568}
		case m.NormalizedModel == "E-410" && p.cameraID == "S0013" && plan.layout == "olympus-legacy-predictive12" &&
			p.mode == "SpecialMode=2 1 0;DriveMode=1 1 0":
			width, height, white = 3720, 2800, 3946
			coefficients = [9]int{8856, -2582, -1026, -7761, 15766, 2082, -2009, 2575, 7469}
		case m.NormalizedModel == "E-510" && p.cameraID == "S0010" && plan.layout == "olympus-legacy-predictive12" &&
			p.mode == "SpecialMode=0 0 0;DriveMode=0 0 0":
			width, height, white = 3720, 2800, 3946
			coefficients = [9]int{8785, -2529, -1033, -7639, 15624, 2112, -1783, 2300, 7817}
		}
	}
	if width != 0 && plan.width == width && plan.height == height {
		source := "LibRaw-0.21.4/" + m.NormalizedModel + "/fixture-mode-profile"
		if !m.Calibration.White.Present() {
			whiteSource := source + "/colordata-maximum"
			if origin == OriginEstimate {
				whiteSource = source + "/12-bit-default-not-measured"
			}
			m.Calibration.White = Value[Levels]{Value: Levels{{float64(white), float64(white)}, {float64(white), float64(white)}}, Origin: origin, Source: whiteSource}
		}
		if !m.Calibration.Color.Present() {
			color, ok := profileColor(coefficients)
			if !ok {
				return &Error{Op: "camera profile", Kind: ErrCalibrationUnavailable, Detail: "invalid approved profile transform"}
			}
			m.Calibration.Color = Value[ColorTransform]{Value: color, Origin: OriginProfile, Source: source}
		}
	}
	if !m.Calibration.White.Present() {
		p.diagnostic("white-level-unavailable", "no verified per-file white level or approved production profile", "calibration")
	} else if m.Calibration.White.Origin == OriginEstimate {
		p.diagnostic("white-level-estimated", "reference bit-depth default, not measured saturation; rendering requires explicit consent or override", m.Calibration.White.Source)
	}
	if !m.Calibration.Color.Present() {
		p.diagnostic("color-transform-unavailable", "no verified camera RGB to XYZ transform for this identity/mode/geometry", "calibration")
	}
	return nil
}

func profileColor(coefficients [9]int) (ColorTransform, bool) {
	var xyzToCamera matrix3
	for i := range 3 {
		for j := range 3 {
			xyzToCamera[i][j] = float64(float32(coefficients[3*i+j]) / 10000)
		}
	}
	inverse, ok := inverse3(xyzToCamera)
	if !ok {
		return ColorTransform{}, false
	}
	// Normalize the XYZ-to-camera rows at D65 before inversion: the public
	// transform consumes white-balanced camera RGB, not unbalanced sensor RGB.
	response := transform3(xyzToCamera, d65White())
	for i := range 3 {
		if !finite(response[i]) || response[i] <= 0 {
			return ColorTransform{}, false
		}
		for j := range 3 {
			inverse[j][i] *= response[i]
		}
	}
	return ColorTransform{CameraToXYZ: [3][3]float64(inverse), ReferenceWhite: d65White()}, true
}
