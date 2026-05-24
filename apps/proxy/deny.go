package main

import (
	"regexp"
	"strings"
)

// genericPermissionDenied is the only error message ever returned to clients.
// No information about which table/column/policy caused the denial may leak.
const genericPermissionDenied = "permission denied for query"

// bannedPatterns is checked on raw SQL before any parsing.
// Matches are fast-path denied without calling PDP or Calcite.
var bannedPatterns = []*regexp.Regexp{
	// System catalog tables
	regexp.MustCompile(`(?i)\bpg_catalog\b`),
	regexp.MustCompile(`(?i)\binformation_schema\b`),
	regexp.MustCompile(`(?i)\bpg_class\b`),
	regexp.MustCompile(`(?i)\bpg_namespace\b`),
	regexp.MustCompile(`(?i)\bpg_attribute\b`),
	regexp.MustCompile(`(?i)\bpg_authid\b`),
	regexp.MustCompile(`(?i)\bpg_proc\b`),
	regexp.MustCompile(`(?i)\bpg_stat_\w+`),
	// Side-effecting / time-channel functions
	regexp.MustCompile(`(?i)\bpg_sleep\s*\(`),
	regexp.MustCompile(`(?i)\bpg_read_file\s*\(`),
	regexp.MustCompile(`(?i)\bpg_ls_dir\s*\(`),
	regexp.MustCompile(`(?i)\blo_import\s*\(`),
	regexp.MustCompile(`(?i)\blo_export\s*\(`),
	regexp.MustCompile(`(?i)\bcopy\s+\w`),
	// DDL / DML (allow only SELECT in V1)
	regexp.MustCompile(`(?i)^\s*(insert|update|delete|truncate|drop|alter|create|grant|revoke|comment)\b`),
	// SET ROLE / SET app.* from client side
	regexp.MustCompile(`(?i)\bset\s+(local\s+)?role\b`),
	regexp.MustCompile(`(?i)\bset\s+(local\s+)?app\.\w+`),
	// Multi-statement (semicolons outside string literals — rough check)
	regexp.MustCompile(`;[^']*$`),
}

// denylistCheck returns a non-empty reason string if the SQL is denied.
// The reason is for audit only; clients always see genericPermissionDenied.
func denylistCheck(sql string) string {
	// Normalise line continuations.
	normalised := strings.ReplaceAll(sql, "\n", " ")
	normalised = strings.ReplaceAll(normalised, "\r", " ")

	for _, pat := range bannedPatterns {
		if pat.MatchString(normalised) {
			return "denylist:" + pat.String()
		}
	}
	return ""
}

// extractCandidateTables does a best-effort regex extraction of table names
// from a SELECT statement. Used to feed the PDP BulkDecide call before Calcite
// parsing. Calcite will be authoritative; this is a fast pre-filter.
var fromTablePattern = regexp.MustCompile(
	`(?i)\b(?:from|join)\s+(?:"?(\w+)"?\."?(\w+)"?|"?(\w+)"?)(?:\s+(?:as\s+)?\w+)?`)

func extractCandidateTables(sql string) []string {
	seen := map[string]bool{}
	var tables []string
	matches := fromTablePattern.FindAllStringSubmatch(sql, -1)
	for _, m := range matches {
		var name string
		if m[3] != "" {
			name = strings.ToLower(m[3])
		} else {
			name = strings.ToLower(m[2])
		}
		if !seen[name] && name != "" {
			seen[name] = true
			tables = append(tables, name)
		}
	}
	return tables
}

// isSelectStatement returns true only if the SQL starts with SELECT (after trimming).
// Non-SELECT queries are denied in V1.
func isSelectStatement(sql string) bool {
	trimmed := strings.TrimSpace(sql)
	return regexp.MustCompile(`(?i)^(with\s+.+\s+)?select\b`).MatchString(trimmed)
}
