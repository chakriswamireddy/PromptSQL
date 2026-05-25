// Package scoring converts per-dimension z-scores into a single 0–100 risk score.
package scoring

import (
	"encoding/json"
	"math"
	"time"
)

const modelVersion = "stat-v1.0.0"

// DefaultWeights are used when no per-tenant calibration exists.
// Weights must sum to 1.0.
var DefaultWeights = map[string]float64{
	"time_of_day":      0.25,
	"day_of_week":      0.10,
	"resource_novelty": 0.25,
	"row_volume":       0.20,
	"ip_drift":         0.20,
}

// RiskScore is the scored output written to Redis and Kafka.
type RiskScore struct {
	TenantID     string             `json:"tenant_id"`
	UserID       string             `json:"user_id"`
	Score        int                `json:"score"`
	Components   map[string]float64 `json:"components"`
	DecayedTotal int                `json:"decayed_total"`
	ComputedAt   time.Time          `json:"computed_at"`
	ModelVersion string             `json:"model_version"`
}

// Compute derives a 0–100 risk score from z-scores, weights, and a sensitivity multiplier.
// zscores: per-dimension anomaly signals (from UserBaseline.ZScores).
// weights: per-dimension weights (should sum to 1.0).
// sensitivityMult: 1 + classificationWeight for the accessed resource.
// prevScore: the current stored score (for decay blending).
// decayHalfLifeHours: exponential decay half-life.
// elapsed: time since the previous score was computed.
func Compute(
	tenantID, userID string,
	zscores map[string]float64,
	weights map[string]float64,
	sensitivityMult float64,
	prevScore int,
	decayHalfLifeHours float64,
	elapsed time.Duration,
) RiskScore {
	if weights == nil {
		weights = DefaultWeights
	}

	// Weighted sum of z-scores (0–3 range per dimension, weights sum to 1).
	// Result is 0–3 before scaling.
	rawScore := 0.0
	components := make(map[string]float64, len(zscores))
	for dim, z := range zscores {
		w := weights[dim]
		if w == 0 {
			w = DefaultWeights[dim]
		}
		contribution := z * w
		components[dim] = math.Round(contribution*1000) / 1000
		rawScore += contribution
	}

	// Apply resource sensitivity multiplier.
	rawScore *= sensitivityMult

	// Scale to 0–100 (z-score max is 3.0 × multiplier max 2.0 = 6.0 → map to 100).
	newScore := int(math.Min(rawScore/6.0*100, 100))

	// Decay the previous score.
	decayed := decayScore(prevScore, decayHalfLifeHours, elapsed)

	// The final score is the max of the new event score and the decayed total.
	// This ensures a spike registers even if the decayed total was lower.
	finalScore := newScore
	if decayed > finalScore {
		finalScore = decayed
	}

	return RiskScore{
		TenantID:     tenantID,
		UserID:       userID,
		Score:        newScore,
		Components:   components,
		DecayedTotal: finalScore,
		ComputedAt:   time.Now().UTC(),
		ModelVersion: modelVersion,
	}
}

// decayScore applies exponential decay to prevScore.
// halfLifeHours: time for score to halve.
// elapsed: time since last score computation.
func decayScore(prevScore int, halfLifeHours float64, elapsed time.Duration) int {
	if prevScore == 0 || halfLifeHours <= 0 {
		return 0
	}
	hours := elapsed.Hours()
	decayFactor := math.Pow(0.5, hours/halfLifeHours)
	return int(float64(prevScore) * decayFactor)
}

// Marshal serialises a RiskScore to JSON for Redis/Kafka.
func (rs RiskScore) Marshal() ([]byte, error) {
	return json.Marshal(rs)
}
