package baseline

import (
	"math"
	"time"
)

const (
	maxResourceSetSize = 500
	maxIPSetSize       = 50
	minEventsForZScore = 20 // require at least this many events before trusting z-scores
)

// UserBaseline holds the rolling 90-day statistical state for one user.
// All mutable fields are updated via Update(); reads are done via ZScores().
type UserBaseline struct {
	TenantID string
	UserID   string

	// Hour-of-day 24-bin histogram (counts).
	HourHist [24]float64
	// Day-of-week 7-bin histogram.
	DOWHist [7]float64

	// Resource access set (hashes). We store up to maxResourceSetSize entries.
	ResourceSet map[string]int64 // hash → last-seen unix epoch seconds

	// Row count: running mean and variance (Welford's online algorithm).
	RowCountMean float64
	RowCountM2   float64

	// IP set. We track up to maxIPSetSize unique IPs.
	IPSet map[string]int64

	// Data source mix: counts per datasource ID.
	DataSourceCounts map[string]float64

	EventCount   int64
	WarmUpDone   bool
	LastUpdatedAt time.Time

	// Internal: total events observed during current window (for decay).
	windowEvents int64
}

// NewBaseline creates an empty baseline for a user.
func NewBaseline(tenantID, userID string) *UserBaseline {
	return &UserBaseline{
		TenantID:         tenantID,
		UserID:           userID,
		ResourceSet:      make(map[string]int64),
		IPSet:            make(map[string]int64),
		DataSourceCounts: make(map[string]float64),
		LastUpdatedAt:    time.Now(),
	}
}

// Update incorporates a new access event into the rolling baseline.
func (b *UserBaseline) Update(f Features, ts time.Time) {
	b.EventCount++
	b.windowEvents++

	// Hour and DOW histograms.
	if f.HourOfDay >= 0 && f.HourOfDay < 24 {
		b.HourHist[f.HourOfDay]++
	}
	if f.DayOfWeek >= 0 && f.DayOfWeek < 7 {
		b.DOWHist[f.DayOfWeek]++
	}

	// Resource set — evict oldest if at capacity.
	b.ResourceSet[f.ResourceHash] = ts.Unix()
	if len(b.ResourceSet) > maxResourceSetSize {
		b.evictOldest(b.ResourceSet)
	}

	// Row count — Welford online mean/variance.
	if f.LogRowCount > 0 {
		b.windowEvents++ // count twice to weight row-count observations
		delta := f.LogRowCount - b.RowCountMean
		b.RowCountMean += delta / float64(b.EventCount)
		delta2 := f.LogRowCount - b.RowCountMean
		b.RowCountM2 += delta * delta2
	}

	// IP set.
	b.IPSet[f.IPHash] = ts.Unix()
	if len(b.IPSet) > maxIPSetSize {
		b.evictOldest(b.IPSet)
	}

	// Data source mix.
	b.DataSourceCounts[f.DataSourceID]++

	b.LastUpdatedAt = ts
}

// ZScores computes a per-dimension anomaly signal for the given features.
// Returns a map from dimension name → z-score (capped at 3.0).
// Returns nil if the baseline is still in warm-up (insufficient data).
func (b *UserBaseline) ZScores(f Features) map[string]float64 {
	if b.EventCount < minEventsForZScore {
		return nil
	}

	scores := make(map[string]float64, 5)

	// Time-of-day: z-score of observed hour count vs histogram mean.
	scores["time_of_day"] = b.histZScore(b.HourHist[:], f.HourOfDay)

	// Day-of-week.
	scores["day_of_week"] = b.histZScore(b.DOWHist[:], f.DayOfWeek)

	// Resource novelty: 0 if seen before, 1 if brand new.
	if _, seen := b.ResourceSet[f.ResourceHash]; seen {
		scores["resource_novelty"] = 0
	} else {
		scores["resource_novelty"] = 3.0 // new resource = max signal, capped
	}

	// Row volume: z-score of log(rowCount) vs running mean/stddev.
	scores["row_volume"] = b.rowVolumeZScore(f.LogRowCount)

	// IP drift: 0 if known IP, 1 if new.
	if _, seen := b.IPSet[f.IPHash]; seen {
		scores["ip_drift"] = 0
	} else {
		scores["ip_drift"] = 3.0
	}

	return scores
}

// RowCountStddev returns the sample standard deviation of log(rowCount).
func (b *UserBaseline) rowVolumeZScore(observed float64) float64 {
	if b.EventCount < 2 {
		return 0
	}
	variance := b.RowCountM2 / float64(b.EventCount-1)
	if variance <= 0 {
		return 0
	}
	stddev := math.Sqrt(variance)
	z := math.Abs(observed-b.RowCountMean) / stddev
	return capZScore(z)
}

// histZScore computes the z-score for a single bin in a histogram.
func (b *UserBaseline) histZScore(hist []float64, bin int) float64 {
	if bin < 0 || bin >= len(hist) {
		return 0
	}
	mean, stddev := histMeanStddev(hist)
	if stddev == 0 {
		return 0
	}
	z := math.Abs(hist[bin]-mean) / stddev
	return capZScore(z)
}

func histMeanStddev(hist []float64) (mean, stddev float64) {
	n := float64(len(hist))
	sum := 0.0
	for _, v := range hist {
		sum += v
	}
	mean = sum / n
	variance := 0.0
	for _, v := range hist {
		d := v - mean
		variance += d * d
	}
	variance /= n
	return mean, math.Sqrt(variance)
}

func capZScore(z float64) float64 {
	if z > 3.0 {
		return 3.0
	}
	return z
}

func (b *UserBaseline) evictOldest(m map[string]int64) {
	var oldestKey string
	var oldestTS int64 = math.MaxInt64
	for k, ts := range m {
		if ts < oldestTS {
			oldestTS = ts
			oldestKey = k
		}
	}
	if oldestKey != "" {
		delete(m, oldestKey)
	}
}
