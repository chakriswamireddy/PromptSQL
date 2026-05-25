package scoring

import (
	"testing"
	"time"
)

// TestComputeScoreRange verifies the output score is always 0–100.
func TestComputeScoreRange(t *testing.T) {
	cases := []struct {
		name    string
		zscores map[string]float64
		mult    float64
		prev    int
	}{
		{"all zeros", map[string]float64{"time_of_day": 0, "day_of_week": 0, "resource_novelty": 0, "row_volume": 0, "ip_drift": 0}, 1.0, 0},
		{"all max", map[string]float64{"time_of_day": 3, "day_of_week": 3, "resource_novelty": 3, "row_volume": 3, "ip_drift": 3}, 2.0, 0},
		{"restricted spike", map[string]float64{"ip_drift": 3, "resource_novelty": 3}, 2.0, 50},
		{"normal with prev", map[string]float64{"time_of_day": 0.5}, 1.0, 80},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := Compute("t1", "u1", tc.zscores, nil, tc.mult, tc.prev, 1.0, time.Hour)
			if rs.Score < 0 || rs.Score > 100 {
				t.Errorf("score out of range: %d", rs.Score)
			}
			if rs.DecayedTotal < 0 || rs.DecayedTotal > 100 {
				t.Errorf("decayed_total out of range: %d", rs.DecayedTotal)
			}
			if rs.ModelVersion == "" {
				t.Error("expected non-empty model_version")
			}
		})
	}
}

// TestDecayReducesScore verifies that a high previous score decays over time.
func TestDecayReducesScore(t *testing.T) {
	// Minimal zscores = no new event contribution.
	zscores := map[string]float64{"time_of_day": 0}

	// After 2 half-lives, score should be roughly prevScore * 0.25
	rs := Compute("t1", "u1", zscores, nil, 1.0, 80, 1.0, 2*time.Hour)

	// Decayed should be ~20 (80 * 0.5^2).
	// Allow ±5 for floating point rounding.
	if rs.DecayedTotal > 25 || rs.DecayedTotal < 15 {
		t.Errorf("expected ~20 after 2 half-lives of 80, got %d", rs.DecayedTotal)
	}
}

// TestSensitivityMultiplier verifies classification weights scale scores.
func TestSensitivityMultiplier(t *testing.T) {
	zscores := map[string]float64{"resource_novelty": 1.0}

	rsPublic := Compute("t1", "u1", zscores, nil, 1.0, 0, 1.0, 0)
	rsRestricted := Compute("t1", "u1", zscores, nil, 2.0, 0, 1.0, 0)

	if rsRestricted.Score <= rsPublic.Score {
		t.Errorf("restricted score (%d) should be higher than public (%d)", rsRestricted.Score, rsPublic.Score)
	}
}
