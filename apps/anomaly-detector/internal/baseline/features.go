// Package baseline computes per-user behavioral baselines and z-score anomaly signals.
package baseline

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// AccessEvent is the minimal representation of an audit.access Kafka message
// consumed by the anomaly detector.
type AccessEvent struct {
	EventID      string    `json:"event_id"`
	TenantID     string    `json:"tenant_id"`
	UserID       string    `json:"user_id"`
	Resource     string    `json:"resource"`
	Action       string    `json:"action"`
	DataSourceID string    `json:"data_source_id"`
	RowCount     int64     `json:"row_count"`
	IPAddress    string    `json:"ip_address"`
	Classification string  `json:"classification"` // public|internal|confidential|restricted
	EventTime    time.Time `json:"event_time"`
}

// Features are the scalar signals extracted from a single access event for
// comparison against the user's rolling baseline.
type Features struct {
	HourOfDay    int    // 0–23
	DayOfWeek    int    // 0–6 (Sunday=0)
	ResourceHash string // sha256[:16] of resource name
	LogRowCount  float64
	IPHash       string // sha256[:16] of IP
	DataSourceID string
	Classification string
}

// ClassificationWeight maps data sensitivity to a score multiplier.
// Accessing restricted data amplifies the anomaly contribution.
var ClassificationWeight = map[string]float64{
	"public":       0.0,
	"internal":     0.2,
	"confidential": 0.5,
	"restricted":   1.0,
}

// Extract derives Features from an AccessEvent.
func Extract(ev AccessEvent) Features {
	logRC := float64(0)
	if ev.RowCount > 0 {
		logRC = logSafe(float64(ev.RowCount))
	}

	class := ev.Classification
	if _, ok := ClassificationWeight[class]; !ok {
		class = "internal"
	}

	return Features{
		HourOfDay:      ev.EventTime.UTC().Hour(),
		DayOfWeek:      int(ev.EventTime.UTC().Weekday()),
		ResourceHash:   hashShort(ev.Resource),
		LogRowCount:    logRC,
		IPHash:         hashShort(ev.IPAddress),
		DataSourceID:   ev.DataSourceID,
		Classification: class,
	}
}

// SensitivityMultiplier returns 1 + classificationWeight for a given class.
func SensitivityMultiplier(class string) float64 {
	w, ok := ClassificationWeight[class]
	if !ok {
		return 1.2 // unknown → treat as internal
	}
	return 1.0 + w
}

func hashShort(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

func logSafe(v float64) float64 {
	if v <= 0 {
		return 0
	}
	// natural log approximation via repeated halving avoids math import cycle
	result := 0.0
	for v > 1 {
		v /= 2
		result += 0.693147
	}
	return result
}
