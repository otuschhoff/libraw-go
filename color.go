// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import "math"

type matrix3 [3][3]float64

func rgbToXYZ() matrix3 {
	return matrix3{{0.4124564, 0.3575761, 0.1804375}, {0.2126729, 0.7151522, 0.0721750}, {0.0193339, 0.1191920, 0.9503041}}
}

func d65White() [3]float64 { return [3]float64{0.95047, 1, 1.08883} }

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func multiply3(a, b matrix3) (out matrix3) {
	for i := range 3 {
		for j := range 3 {
			for k := range 3 {
				out[i][j] += a[i][k] * b[k][j]
			}
		}
	}
	return out
}

func transform3(a matrix3, v [3]float64) (out [3]float64) {
	for i := range 3 {
		for j := range 3 {
			out[i] += a[i][j] * v[j]
		}
	}
	return out
}

func inverse3(a matrix3) (matrix3, bool) {
	scale := 0.0
	for _, row := range a {
		for _, value := range row {
			if !finite(value) {
				return matrix3{}, false
			}
			scale = max(scale, math.Abs(value))
		}
	}
	if scale == 0 {
		return matrix3{}, false
	}
	for i := range 3 {
		for j := range 3 {
			a[i][j] /= scale
		}
	}
	out := matrix3{
		{a[1][1]*a[2][2] - a[1][2]*a[2][1], a[0][2]*a[2][1] - a[0][1]*a[2][2], a[0][1]*a[1][2] - a[0][2]*a[1][1]},
		{a[1][2]*a[2][0] - a[1][0]*a[2][2], a[0][0]*a[2][2] - a[0][2]*a[2][0], a[0][2]*a[1][0] - a[0][0]*a[1][2]},
		{a[1][0]*a[2][1] - a[1][1]*a[2][0], a[0][1]*a[2][0] - a[0][0]*a[2][1], a[0][0]*a[1][1] - a[0][1]*a[1][0]},
	}
	det := a[0][0]*out[0][0] + a[0][1]*out[1][0] + a[0][2]*out[2][0]
	if !finite(det) || math.Abs(det) < 1e-12 {
		return matrix3{}, false
	}
	for i := range 3 {
		for j := range 3 {
			out[i][j] = (out[i][j] / det) / scale
			if !finite(out[i][j]) {
				return matrix3{}, false
			}
		}
	}
	return out, true
}

func renderColorMatrix(color ColorTransform) (matrix3, bool) {
	white := color.ReferenceWhite
	for _, value := range white {
		if !finite(value) || value <= 0 {
			return matrix3{}, false
		}
	}
	if math.Abs(white[1]-1) > 1e-6 {
		return matrix3{}, false
	}
	camera := matrix3(color.CameraToXYZ)
	if _, ok := inverse3(camera); !ok {
		return matrix3{}, false
	}
	adapt := matrix3{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	if white != d65White() {
		bradford := matrix3{{0.8951, 0.2664, -0.1614}, {-0.7502, 1.7135, 0.0367}, {0.0389, -0.0685, 1.0296}}
		inverse, ok := inverse3(bradford)
		if !ok {
			return matrix3{}, false
		}
		source, target := transform3(bradford, white), transform3(bradford, d65White())
		var diagonal matrix3
		for i := range 3 {
			if source[i] <= 0 || !finite(source[i]) {
				return matrix3{}, false
			}
			diagonal[i][i] = target[i] / source[i]
		}
		adapt = multiply3(multiply3(inverse, diagonal), bradford)
	}
	xyzToRGB, ok := inverse3(rgbToXYZ())
	if !ok {
		return matrix3{}, false
	}
	out := multiply3(multiply3(xyzToRGB, adapt), camera)
	for _, row := range out {
		for _, value := range row {
			if !finite(value) {
				return matrix3{}, false
			}
		}
	}
	return out, true
}

func encodeSRGB(value float64) uint16 {
	value = min(1, max(0, value))
	if value <= 0.0031308 {
		value *= 12.92
	} else {
		value = 1.055*math.Pow(value, 1/2.4) - 0.055
	}
	return uint16(math.Floor(value*65535 + 0.5))
}
