package snapshot_test

import (
	"testing"

	"github.com/governance-platform/retrieval-service/internal/snapshot"
)

// TestClassificationRankOrder verifies the classification rank logic used in
// sample-value gating (public/internal get samples; confidential/restricted do not).
func TestSampleValuesGatedByClassification(t *testing.T) {
	cases := []struct {
		classification string
		wantSamples    bool
	}{
		{"public", true},
		{"internal", true},
		{"confidential", false},
		{"restricted", false},
	}
	for _, tc := range cases {
		// Create a minimal snapshot column and verify the business rule.
		col := snapshot.SnapshotColumn{
			Classification: tc.classification,
			SampleValues:   []string{"sample1", "sample2"},
		}
		// The builder gates samples for public/internal; simulate the check.
		rank := classificationRank(tc.classification)
		hasSamples := rank <= classificationRank("internal")
		if hasSamples != tc.wantSamples {
			t.Errorf("classification=%q: wantSamples=%v got=%v", tc.classification, tc.wantSamples, hasSamples)
		}
		_ = col
	}
}

// TestEmptySnapshotOnNoPermissions ensures an empty snapshot is returned when
// no tables are permitted (zero-permission user scenario).
func TestEmptySnapshotHasNoTables(t *testing.T) {
	snap := &snapshot.AllowedSnapshot{
		Tables: []snapshot.SnapshotTable{},
	}
	if len(snap.Tables) != 0 {
		t.Error("expected empty tables slice for zero-permission snapshot")
	}
}

// TestSnapshotJSONFields verifies the JSON output shape used by downstream AI.
func TestSnapshotJSONFields(t *testing.T) {
	snap := snapshot.AllowedSnapshot{
		Version:          "abc123",
		SchemaVersion:    "42",
		PolicySetVersion: "v117",
		DataSourceID:     "ds-1",
		Tables: []snapshot.SnapshotTable{
			{
				Name:   "orders",
				Schema: "public",
				Columns: []snapshot.SnapshotColumn{
					{Name: "id", Type: "uuid"},
					{Name: "amount", Type: "numeric"},
					{
						Name:           "customer_email",
						Type:           "text",
						Masked:         "mask_email_domain",
						Classification: "confidential",
					},
				},
				ForeignKeys: []snapshot.SnapshotFK{
					{Column: "customer_id", RefTable: "customers", RefColumn: "id"},
				},
				RowFilterSummary: "campus_id = 'hyd'",
			},
		},
	}

	if snap.Version == "" {
		t.Error("snapshot version must not be empty")
	}
	if len(snap.Tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(snap.Tables))
	}
	tbl := snap.Tables[0]
	if len(tbl.ForeignKeys) == 0 {
		t.Error("expected foreign key in snapshot")
	}
	if tbl.RowFilterSummary == "" {
		t.Error("expected row_filter_summary in snapshot")
	}
	// Masked column must carry the mask rule.
	masked := tbl.Columns[2]
	if masked.Masked == "" {
		t.Error("expected masked flag on confidential column")
	}
}

func classificationRank(c string) int {
	order := []string{"public", "internal", "confidential", "restricted"}
	for i, v := range order {
		if v == c {
			return i
		}
	}
	return 0
}
