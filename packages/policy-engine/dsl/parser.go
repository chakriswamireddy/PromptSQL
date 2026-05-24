package dsl

import (
	"encoding/json"
	"fmt"
)

// Parse decodes raw JSON bytes into a typed AST Node.
// It does NOT validate depth/node-count/operators — call Validate separately.
// Returns an error if the JSON is structurally invalid or not a JSON object.
func Parse(data []byte) (*Node, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("dsl: empty condition")
	}
	var n Node
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, fmt.Errorf("dsl: parse: %w", err)
	}
	if !n.IsPredicate() && !n.IsCombinator() {
		return nil, fmt.Errorf("dsl: parse: node has no recognized fields (all/any/not/field)")
	}
	if n.IsPredicate() && n.IsCombinator() {
		return nil, fmt.Errorf("dsl: parse: node cannot be both predicate and combinator")
	}
	return &n, nil
}

// MustParse parses data and panics on error. Use only in tests.
func MustParse(data []byte) *Node {
	n, err := Parse(data)
	if err != nil {
		panic(err)
	}
	return n
}
