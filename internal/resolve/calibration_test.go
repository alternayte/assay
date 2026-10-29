package resolve

import (
	"math"
	"testing"
)

// outcomes gives n reviewed outcomes at raw probability p with the band of
// the decision, the first accepted ones accepted.
func outcomes(n, accepted int, p float64, band string) []Outcome {
	out := make([]Outcome, n)
	for i := range out {
		out[i] = Outcome{RawP: p, Band: band, Accepted: i < accepted}
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSCN_RES_003_CalibrationBandsAndPrecision(t *testing.T) {
	// Fewer than 50 outcomes: default thresholds and not calibrated.
	c := Calibrate(outcomes(49, 10, 0.95, BandHigh))
	if c.Calibrated || c.Reviewed != 49 {
		t.Fatalf("49 outcomes: calibrated %v reviewed %d", c.Calibrated, c.Reviewed)
	}
	for p, want := range map[float64]string{0.95: BandHigh, 0.9: BandHigh, 0.89: BandMedium, 0.6: BandMedium, 0.59: BandLow, 0.05: BandLow} {
		if got := c.Band(p); got != want {
			t.Errorf("default band of %.2f is %s, want %s", p, got, want)
		}
	}

	// 100 outcomes: bin 9 accepts 38 of 40, bin 7 accepts 20 of 30 and bin 2
	// accepts 3 of 30. The Laplace rate of a bin sets its band.
	var os []Outcome
	os = append(os, outcomes(40, 38, 0.95, BandHigh)...)
	os = append(os, outcomes(30, 20, 0.75, BandMedium)...)
	os = append(os, outcomes(30, 3, 0.25, BandLow)...)
	c = Calibrate(os)
	if !c.Calibrated || c.Reviewed != 100 {
		t.Fatalf("100 outcomes: calibrated %v reviewed %d", c.Calibrated, c.Reviewed)
	}
	if got := c.Bins[9]; got.Reviewed != 40 || got.Accepted != 38 {
		t.Errorf("bin 9 is %+v", got)
	}
	for p, want := range map[float64]string{0.97: BandHigh, 0.72: BandMedium, 0.2: BandLow} {
		if got := c.Band(p); got != want {
			t.Errorf("calibrated band of %.2f is %s, want %s", p, got, want)
		}
	}
	// Bin 5 has no outcomes, so 0.55 uses the default rule.
	if got := c.Band(0.55); got != BandLow {
		t.Errorf("an empty bin uses the default band, got %s", got)
	}
	if pr := c.Precision[BandHigh]; pr.Reviewed != 40 || !near(pr.Value, 0.95) {
		t.Errorf("High precision %+v, want 38 of 40", pr)
	}
	if pr := c.Precision[BandMedium]; pr.Reviewed != 30 || !near(pr.Value, 20.0/30) {
		t.Errorf("Medium precision %+v, want 20 of 30", pr)
	}
	if pr := c.Precision[BandLow]; pr.Reviewed != 30 || !near(pr.Value, 0.1) {
		t.Errorf("Low precision %+v, want 3 of 30", pr)
	}

	// Auto-apply needs 50 outcomes and High precision of 0.95 over the last
	// 200. Precision uses the band that the reviewer saw.
	if !c.CanAutoApply() {
		t.Error("38 of 40 High outcomes at 0.95 cannot enable auto-apply")
	}
	if Calibrate(outcomes(49, 49, 0.99, BandHigh)).CanAutoApply() {
		t.Error("auto-apply with 49 outcomes")
	}
	if Calibrate(outcomes(60, 56, 0.99, BandHigh)).CanAutoApply() {
		t.Error("auto-apply at 56 of 60")
	}
	// Only the newest 200 outcomes count. Outcomes are newest first.
	recent := append(outcomes(200, 200, 0.99, BandHigh), outcomes(100, 0, 0.99, BandHigh)...)
	if !Calibrate(recent).CanAutoApply() {
		t.Error("old rejections beyond the last 200 block auto-apply")
	}
}
