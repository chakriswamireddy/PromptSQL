package baseline

import (
	"testing"
	"time"
)

// TestZScoresDeterminism verifies that given the same event stream twice,
// the baseline produces identical z-scores (replay invariant).
func TestZScoresDeterminism(t *testing.T) {
	seed := func() *UserBaseline {
		b := NewBaseline("tenant-1", "user-1")
		base := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
		// Populate a normal working-hours pattern.
		for i := 0; i < 40; i++ {
			ts := base.Add(time.Duration(i) * 24 * time.Hour)
			f := Features{
				HourOfDay:      9,
				DayOfWeek:      int(ts.Weekday()),
				ResourceHash:   "aaaa",
				LogRowCount:    3.0,
				IPHash:         "bbbb",
				DataSourceID:   "ds-1",
				Classification: "internal",
			}
			b.Update(f, ts)
		}
		return b
	}

	b1 := seed()
	b2 := seed()

	anomaly := Features{
		HourOfDay:      2, // unusual hour
		DayOfWeek:      0, // Sunday
		ResourceHash:   "cccc", // new resource
		LogRowCount:    10.0,
		IPHash:         "dddd", // new IP
		DataSourceID:   "ds-2",
		Classification: "restricted",
	}

	z1 := b1.ZScores(anomaly)
	z2 := b2.ZScores(anomaly)

	if z1 == nil || z2 == nil {
		t.Fatal("expected non-nil z-scores after 40 events")
	}
	for dim, v1 := range z1 {
		if v2, ok := z2[dim]; !ok || v1 != v2 {
			t.Errorf("z-score[%s] mismatch: %v vs %v", dim, v1, v2)
		}
	}
}

// TestWarmupGate ensures ZScores returns nil while event count < minEventsForZScore.
func TestWarmupGate(t *testing.T) {
	b := NewBaseline("tenant-1", "user-1")
	f := Features{HourOfDay: 9, DayOfWeek: 1, ResourceHash: "aa", LogRowCount: 1, IPHash: "bb", DataSourceID: "ds-1", Classification: "public"}

	for i := 0; i < minEventsForZScore-1; i++ {
		b.Update(f, time.Now())
	}
	if b.ZScores(f) != nil {
		t.Error("expected nil z-scores below warm-up threshold")
	}

	b.Update(f, time.Now())
	if b.ZScores(f) == nil {
		t.Error("expected non-nil z-scores at warm-up threshold")
	}
}

// TestZScoreCapped verifies z-scores are capped at 3.0.
func TestZScoreCapped(t *testing.T) {
	b := NewBaseline("t1", "u1")
	base := time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC) // Monday 09:00
	for i := 0; i < 50; i++ {
		f := Features{HourOfDay: 9, DayOfWeek: 1, ResourceHash: "aa", LogRowCount: 1, IPHash: "bb", DataSourceID: "d1", Classification: "public"}
		b.Update(f, base.Add(time.Duration(i)*24*time.Hour))
	}

	// Completely out-of-pattern event.
	anomaly := Features{HourOfDay: 3, DayOfWeek: 6, ResourceHash: "zz", LogRowCount: 9, IPHash: "xx", DataSourceID: "d1", Classification: "public"}
	z := b.ZScores(anomaly)
	for dim, v := range z {
		if v > 3.0 {
			t.Errorf("z-score[%s] = %v exceeds cap of 3.0", dim, v)
		}
	}
}

// TestFeatureExtract verifies hour/dow extraction is UTC-consistent.
func TestFeatureExtract(t *testing.T) {
	ts := time.Date(2026, 5, 25, 14, 30, 0, 0, time.UTC) // Monday 14:30 UTC
	ev := AccessEvent{
		TenantID:       "t1",
		UserID:         "u1",
		Resource:       "customers",
		Action:         "SELECT",
		DataSourceID:   "ds-1",
		RowCount:       100,
		IPAddress:      "10.0.0.1",
		Classification: "internal",
		EventTime:      ts,
	}
	f := Extract(ev)
	if f.HourOfDay != 14 {
		t.Errorf("expected HourOfDay=14, got %d", f.HourOfDay)
	}
	if f.DayOfWeek != 1 { // Monday
		t.Errorf("expected DayOfWeek=1 (Monday), got %d", f.DayOfWeek)
	}
	if f.LogRowCount <= 0 {
		t.Error("expected positive log row count")
	}
}
