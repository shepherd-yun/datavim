package sparkline

import "testing"

func TestRender(t *testing.T) {
	for _, tc := range []struct {
		name     string
		vals     []float64
		width    int
		fromZero bool
		want     string
	}{
		{"ramp", []float64{0, 1, 2, 3, 4, 5, 6, 7}, 8, true, "▁▂▃▄▅▆▇█"},
		{"flat padded", []float64{5, 5}, 4, false, "  ▁▁"},
		{"empty", nil, 3, true, "   "},
		{"only the last width", []float64{7, 0, 7}, 2, true, "▁█"},
		{"min max", []float64{10, 11}, 2, false, "▁█"},
		{"zeros from zero", []float64{0, 0}, 2, true, "▁▁"},
		{"no width", []float64{1}, 0, true, ""},
	} {
		if got := Render(tc.vals, tc.width, tc.fromZero); got != tc.want {
			t.Errorf("%s: Render = %q, want %q", tc.name, got, tc.want)
		}
	}
}
