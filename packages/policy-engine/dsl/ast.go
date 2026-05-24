// Package dsl implements the bounded, decidable Conditions DSL used by the PDP.
//
// Grammar (JSON):
//
//	Condition := {"all":[Condition...]} | {"any":[Condition...]} | {"not":Condition} | Predicate
//	Predicate := {"field":Path,"op":Op,"value":Literal}
//	           | {"field":Path,"op":"in","value":[Literal...]}
//	           | {"field":Path,"op":"between","value":[Literal,Literal]}
//	Path      := "subject.<attr>" | "resource.<attr>" | "context.<key>" | "env.<key>"
//	Op        := eq|neq|lt|lte|gt|gte|in|nin|between|startsWith|endsWith|contains|matches
//	Literal   := string | number | bool | iso-date
//
// Constraints enforced by the validator:
//   - Max depth 5
//   - Max nodes 256
//   - Regex: RE2 only, 256-char input cap
//   - No backreferences, loops, function calls, or string interpolation
package dsl

import "encoding/json"

// Op is a predicate comparison operator.
type Op string

const (
	OpEq         Op = "eq"
	OpNeq        Op = "neq"
	OpLt         Op = "lt"
	OpLte        Op = "lte"
	OpGt         Op = "gt"
	OpGte        Op = "gte"
	OpIn         Op = "in"
	OpNin        Op = "nin"
	OpBetween    Op = "between"
	OpStartsWith Op = "startsWith"
	OpEndsWith   Op = "endsWith"
	OpContains   Op = "contains"
	OpMatches    Op = "matches"
)

// AllowedOps is the exhaustive operator allowlist. Any op not in this set is rejected.
var AllowedOps = map[Op]bool{
	OpEq:         true,
	OpNeq:        true,
	OpLt:         true,
	OpLte:        true,
	OpGt:         true,
	OpGte:        true,
	OpIn:         true,
	OpNin:        true,
	OpBetween:    true,
	OpStartsWith: true,
	OpEndsWith:   true,
	OpContains:   true,
	OpMatches:    true,
}

// PathPrefix enumerates the allowed namespace prefixes for field paths.
type PathPrefix string

const (
	PathSubject  PathPrefix = "subject"
	PathResource PathPrefix = "resource"
	PathContext  PathPrefix = "context"
	PathEnv      PathPrefix = "env"
)

// Node is a single node in the Conditions AST.
// A node is either a combinator (All/Any/Not populated) or a predicate (Field/Op/Value populated).
// The zero value is invalid; use Parse to construct a well-typed AST.
type Node struct {
	// Combinator fields — mutually exclusive (only one set at a time).
	All []*Node `json:"all,omitempty"`
	Any []*Node `json:"any,omitempty"`
	Not *Node   `json:"not,omitempty"`

	// Predicate fields — set when the node is a leaf.
	Field string          `json:"field,omitempty"`
	Op    Op              `json:"op,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

// IsPredicate reports whether n is a leaf predicate (vs. a combinator).
func (n *Node) IsPredicate() bool {
	return n.Field != ""
}

// IsCombinator reports whether n has children.
func (n *Node) IsCombinator() bool {
	return len(n.All) > 0 || len(n.Any) > 0 || n.Not != nil
}

// MaxDepth is the maximum nesting depth allowed by the validator.
const MaxDepth = 5

// MaxNodes is the maximum total node count allowed by the validator.
const MaxNodes = 256

// MaxRegexInputLen is the maximum input string length for regex evaluation (ReDoS defence).
const MaxRegexInputLen = 256

// MaxRegexPatternLen is the maximum allowed regex pattern length.
const MaxRegexPatternLen = 128
