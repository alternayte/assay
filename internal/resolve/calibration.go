package resolve

// Bands of a decision.
const (
	BandHigh   = "high"
	BandMedium = "medium"
	BandLow    = "low"
)

// Calibration limits (REQ-RES-004, REQ-RES-005).
const (
	Bins             = 10
	MinReviewed      = 50
	PrecisionWindow  = 200
	AutoApplyMinimum = 0.95
	DefaultHigh      = 0.9
	DefaultMedium    = 0.6
)

// Outcome is one reviewed decision: its raw probability, the band that the
// reviewer saw and the verdict.
type Outcome struct {
	RawP     float64
	Band     string
	Accepted bool
}

// Bin counts the reviewed outcomes of one tenth of the probability range.
type Bin struct {
	Reviewed int `json:"reviewed"`
	Accepted int `json:"accepted"`
}

// Precision is the share of accepted outcomes of one band.
type Precision struct {
	Reviewed int     `json:"reviewed"`
	Value    float64 `json:"value"`
}

// Calibration maps raw probabilities of one kind to bands.
type Calibration struct {
	Bins       []Bin                `json:"bins"`
	Reviewed   int                  `json:"reviewed"`
	Calibrated bool                 `json:"calibrated"`
	Precision  map[string]Precision `json:"precision"`
}

func binOf(p float64) int {
	return min(max(int(p*Bins), 0), Bins-1)
}

// Calibrate builds the calibration of reviewed outcomes, newest first. The
// bins use all outcomes. Precision uses the newest PrecisionWindow.
func Calibrate(os []Outcome) Calibration {
	c := Calibration{Bins: make([]Bin, Bins), Reviewed: len(os), Calibrated: len(os) >= MinReviewed, Precision: map[string]Precision{}}
	for _, o := range os {
		b := &c.Bins[binOf(o.RawP)]
		b.Reviewed++
		if o.Accepted {
			b.Accepted++
		}
	}
	accepted := map[string]int{}
	for i, o := range os {
		if i == PrecisionWindow {
			break
		}
		pr := c.Precision[o.Band]
		pr.Reviewed++
		c.Precision[o.Band] = pr
		if o.Accepted {
			accepted[o.Band]++
		}
	}
	for band, pr := range c.Precision {
		pr.Value = float64(accepted[band]) / float64(pr.Reviewed)
		c.Precision[band] = pr
	}
	return c
}

func defaultBand(p float64) string {
	switch {
	case p >= DefaultHigh:
		return BandHigh
	case p >= DefaultMedium:
		return BandMedium
	}
	return BandLow
}

// Band maps a raw probability to a band. Before MinReviewed outcomes, and
// for a bin with no outcomes, the default thresholds apply to the raw
// probability. Otherwise they apply to the Laplace rate of its bin.
func (c Calibration) Band(p float64) string {
	if !c.Calibrated || len(c.Bins) != Bins {
		return defaultBand(p)
	}
	b := c.Bins[binOf(p)]
	if b.Reviewed == 0 {
		return defaultBand(p)
	}
	return defaultBand(float64(b.Accepted+1) / float64(b.Reviewed+2))
}

// CanAutoApply reports whether the kind has earned auto-apply: MinReviewed
// outcomes and High precision of AutoApplyMinimum or more.
func (c Calibration) CanAutoApply() bool {
	h, ok := c.Precision[BandHigh]
	return c.Reviewed >= MinReviewed && ok && h.Reviewed > 0 && h.Value >= AutoApplyMinimum
}
