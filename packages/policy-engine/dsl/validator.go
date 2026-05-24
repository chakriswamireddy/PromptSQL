package dsl

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"regexp/syntax"
)

// ValidationError is returned when a node fails semantic validation.
type ValidationError struct {
	Path    string
	Message string
}

func (e *ValidationError) Error() string { return fmt.Sprintf("dsl[%s]: %s", e.Path, e.Message) }

// Validate checks depth, node count, operator allowlist, path prefixes, type
// compatibility, and RE2 compilability. Returns nil if the node is valid.
func Validate(n *Node) error {
	state := &validationState{}
	return validateNode(n, 0, "$", state)
}

type validationState struct {
	nodeCount int
}

func validateNode(n *Node, depth int, path string, state *validationState) error {
	if n == nil {
		return &ValidationError{Path: path, Message: "nil node"}
	}
	state.nodeCount++
	if state.nodeCount > MaxNodes {
		return &ValidationError{Path: path, Message: fmt.Sprintf("node count %d exceeds maximum %d", state.nodeCount, MaxNodes)}
	}
	if depth > MaxDepth {
		return &ValidationError{Path: path, Message: fmt.Sprintf("depth %d exceeds maximum %d", depth, MaxDepth)}
	}

	// Combinators
	if len(n.All) > 0 {
		for i, child := range n.All {
			if err := validateNode(child, depth+1, fmt.Sprintf("%s.all[%d]", path, i), state); err != nil {
				return err
			}
		}
		return nil
	}
	if len(n.Any) > 0 {
		for i, child := range n.Any {
			if err := validateNode(child, depth+1, fmt.Sprintf("%s.any[%d]", path, i), state); err != nil {
				return err
			}
		}
		return nil
	}
	if n.Not != nil {
		return validateNode(n.Not, depth+1, path+".not", state)
	}

	// Predicate
	if !n.IsPredicate() {
		return &ValidationError{Path: path, Message: "empty node (no combinator or predicate fields)"}
	}
	if err := validateField(n.Field, path); err != nil {
		return err
	}
	if !AllowedOps[n.Op] {
		return &ValidationError{Path: path, Message: fmt.Sprintf("unknown operator %q", n.Op)}
	}
	if err := validateValue(n, path); err != nil {
		return err
	}
	return nil
}

func validateField(field, path string) error {
	if field == "" {
		return &ValidationError{Path: path, Message: "field is empty"}
	}
	parts := strings.SplitN(field, ".", 2)
	if len(parts) != 2 || parts[1] == "" {
		return &ValidationError{Path: path, Message: fmt.Sprintf("field %q must be <prefix>.<attr>", field)}
	}
	switch PathPrefix(parts[0]) {
	case PathSubject, PathResource, PathContext, PathEnv:
	default:
		return &ValidationError{Path: path, Message: fmt.Sprintf("unknown field prefix %q (allowed: subject, resource, context, env)", parts[0])}
	}
	return nil
}

func validateValue(n *Node, path string) error {
	if len(n.Value) == 0 {
		return &ValidationError{Path: path, Message: "value is required"}
	}

	switch n.Op {
	case OpIn, OpNin:
		var arr []json.RawMessage
		if err := json.Unmarshal(n.Value, &arr); err != nil {
			return &ValidationError{Path: path, Message: fmt.Sprintf("op %q requires array value: %v", n.Op, err)}
		}
		if len(arr) == 0 {
			return &ValidationError{Path: path, Message: fmt.Sprintf("op %q requires non-empty array", n.Op)}
		}
		for i, el := range arr {
			if err := validateLiteral(el, fmt.Sprintf("%s.value[%d]", path, i)); err != nil {
				return err
			}
		}

	case OpBetween:
		var arr []json.RawMessage
		if err := json.Unmarshal(n.Value, &arr); err != nil {
			return &ValidationError{Path: path, Message: fmt.Sprintf("op 'between' requires 2-element array: %v", err)}
		}
		if len(arr) != 2 {
			return &ValidationError{Path: path, Message: "op 'between' requires exactly 2 elements"}
		}
		for i, el := range arr {
			if err := validateLiteral(el, fmt.Sprintf("%s.value[%d]", path, i)); err != nil {
				return err
			}
		}

	case OpMatches:
		var pattern string
		if err := json.Unmarshal(n.Value, &pattern); err != nil {
			return &ValidationError{Path: path, Message: fmt.Sprintf("op 'matches' requires string pattern: %v", err)}
		}
		if len(pattern) > MaxRegexPatternLen {
			return &ValidationError{Path: path, Message: fmt.Sprintf("regex pattern length %d exceeds maximum %d", len(pattern), MaxRegexPatternLen)}
		}
		// Validate as RE2 (no lookahead, backreferences, etc.)
		if _, err := syntax.Parse(pattern, syntax.Perl); err != nil {
			return &ValidationError{Path: path, Message: fmt.Sprintf("invalid RE2 pattern: %v", err)}
		}
		// Ensure it compiles in Go's regexp package (which uses RE2)
		if _, err := regexp.Compile(pattern); err != nil {
			return &ValidationError{Path: path, Message: fmt.Sprintf("regex compile error: %v", err)}
		}

	default:
		if err := validateLiteral(n.Value, path+".value"); err != nil {
			return err
		}
	}
	return nil
}

func validateLiteral(raw json.RawMessage, path string) error {
	if len(raw) == 0 {
		return &ValidationError{Path: path, Message: "empty literal"}
	}
	// Disallow objects and arrays as scalar literals (only string, number, bool, null).
	// (In/between arrays are handled separately above.)
	switch raw[0] {
	case '{', '[':
		return &ValidationError{Path: path, Message: "literal cannot be an object or array"}
	case '"':
		// string — check for template interpolation signs (${ or {{)
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return &ValidationError{Path: path, Message: fmt.Sprintf("invalid string literal: %v", err)}
		}
		if strings.Contains(s, "${") || strings.Contains(s, "{{") {
			return &ValidationError{Path: path, Message: "string interpolation is not allowed in DSL literals"}
		}
		if !utf8.ValidString(s) {
			return &ValidationError{Path: path, Message: "string literal contains invalid UTF-8"}
		}
	}
	return nil
}
