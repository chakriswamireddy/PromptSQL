package classifier_test

import (
	"testing"

	"github.com/governance-platform/schema-crawler/internal/classifier"
)

func TestSuggest_PIIPatterns(t *testing.T) {
	s := classifier.Default()

	cases := []struct {
		col     string
		dt      string
		wantCls string
		wantTag string
	}{
		{"user_email", "text", "confidential", "contact"},
		{"ssn_number", "text", "restricted", "pii"},
		{"credit_card_number", "text", "restricted", "pci"},
		{"date_of_birth", "date", "confidential", "pii"},
		{"phone_mobile", "text", "confidential", "contact"},
		{"salary_usd", "numeric", "confidential", "financial"},
		{"api_key", "text", "restricted", "credential"},
		{"health_record_id", "text", "restricted", "phi"},
		{"user_id", "uuid", "internal", "identifier"},
	}

	for _, tc := range cases {
		t.Run(tc.col, func(t *testing.T) {
			got := s.Suggest(tc.col, tc.dt)
			if got == nil {
				t.Fatalf("Suggest(%q, %q): got nil, want classification %q", tc.col, tc.dt, tc.wantCls)
			}
			if got.Classification != tc.wantCls {
				t.Errorf("classification: got %q, want %q", got.Classification, tc.wantCls)
			}
			found := false
			for _, tag := range got.Tags {
				if tag == tc.wantTag {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("tag %q not found in %v", tc.wantTag, got.Tags)
			}
			if got.ClassifiedBy != "pattern" {
				t.Errorf("ClassifiedBy: got %q, want %q", got.ClassifiedBy, "pattern")
			}
		})
	}
}

func TestSuggest_NoMatch(t *testing.T) {
	s := classifier.Default()
	got := s.Suggest("widget_count", "integer")
	if got != nil {
		t.Fatalf("expected nil for generic column, got %+v", got)
	}
}

func TestShouldSample(t *testing.T) {
	if !classifier.ShouldSample("public") {
		t.Error("public should be sampleable")
	}
	if !classifier.ShouldSample("internal") {
		t.Error("internal should be sampleable")
	}
	if classifier.ShouldSample("confidential") {
		t.Error("confidential should NOT be sampleable")
	}
	if classifier.ShouldSample("restricted") {
		t.Error("restricted should NOT be sampleable")
	}
}
