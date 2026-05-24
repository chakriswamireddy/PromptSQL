// Package classifier provides pattern-based classification suggestions.
package classifier

import (
	"regexp"
	"strings"
)

// Suggestion is a classification recommendation for a column.
type Suggestion struct {
	Classification string
	Tags           []string
	ClassifiedBy   string
}

// rule maps a column-name regex to a suggested classification.
type rule struct {
	re             *regexp.Regexp
	classification string
	tags           []string
}

// defaultRules are the built-in pattern rules (versioned in this file).
var defaultRules = []rule{
	{re: regexp.MustCompile(`(?i)ssn|social.?security`),                  classification: "restricted", tags: []string{"pii"}},
	{re: regexp.MustCompile(`(?i)credit.?card|card.?number|ccn`),         classification: "restricted", tags: []string{"pci"}},
	{re: regexp.MustCompile(`(?i)cvv|cvc|security.?code`),                classification: "restricted", tags: []string{"pci"}},
	{re: regexp.MustCompile(`(?i)passport|national.?id|tax.?id|ein|itin`), classification: "restricted", tags: []string{"pii", "government_id"}},
	{re: regexp.MustCompile(`(?i)email|e.?mail`),                          classification: "confidential", tags: []string{"contact", "pii"}},
	{re: regexp.MustCompile(`(?i)phone|mobile|cell.?number`),              classification: "confidential", tags: []string{"contact", "pii"}},
	{re: regexp.MustCompile(`(?i)dob|date.?of.?birth|birth.?date`),       classification: "confidential", tags: []string{"pii"}},
	{re: regexp.MustCompile(`(?i)address|street|postal.?code|zip`),        classification: "confidential", tags: []string{"pii", "location"}},
	{re: regexp.MustCompile(`(?i)salary|income|wage|compensation`),        classification: "confidential", tags: []string{"financial"}},
	{re: regexp.MustCompile(`(?i)password|passwd|secret|api.?key|token`),  classification: "restricted", tags: []string{"credential"}},
	{re: regexp.MustCompile(`(?i)health|diagnosis|prescription|medical`),  classification: "restricted", tags: []string{"phi"}},
	{re: regexp.MustCompile(`(?i)ip.?address|ipv[46]`),                   classification: "confidential", tags: []string{"pii", "network"}},
	{re: regexp.MustCompile(`(?i)first.?name|last.?name|full.?name|given.?name`), classification: "confidential", tags: []string{"pii"}},
}

// Suggester evaluates column name and data-type patterns to propose a classification.
type Suggester struct {
	rules []rule
}

// Default returns a Suggester with the built-in rule set.
func Default() *Suggester {
	return &Suggester{rules: defaultRules}
}

// Suggest returns the best matching classification for a column, or nil if no rule fires.
func (s *Suggester) Suggest(columnName, dataType string) *Suggestion {
	name := strings.ToLower(columnName)
	for _, r := range s.rules {
		if r.re.MatchString(name) {
			return &Suggestion{
				Classification: r.classification,
				Tags:           r.tags,
				ClassifiedBy:   "pattern",
			}
		}
	}

	// Data-type heuristics.
	dt := strings.ToLower(dataType)
	if strings.Contains(name, "amount") && (strings.Contains(dt, "numeric") || strings.Contains(dt, "decimal")) {
		return &Suggestion{Classification: "confidential", Tags: []string{"financial"}, ClassifiedBy: "pattern"}
	}

	// UUID columns ending in _id are typically internal references.
	if strings.HasSuffix(name, "_id") && strings.Contains(dt, "uuid") {
		return &Suggestion{Classification: "internal", Tags: []string{"identifier"}, ClassifiedBy: "pattern"}
	}

	return nil
}

// ShouldSample returns true if the classification level allows value sampling.
// Confidential and restricted columns are never sampled.
func ShouldSample(classification string) bool {
	switch classification {
	case "public", "internal":
		return true
	default:
		return false
	}
}
