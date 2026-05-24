package dsl

import (
	"encoding/json"
	"fmt"
)

// SQLPredicate is a dialect-neutral predicate fragment consumed by the Phase 6 Calcite sidecar.
// It mirrors Calcite's RexNode JSON envelope closely enough for direct translation.
type SQLPredicate struct {
	Op       string         `json:"op"`
	Operands []SQLPredicate `json:"operands,omitempty"`
	// For leaf predicates: the SQL column reference.
	Column string `json:"column,omitempty"`
	// For leaf predicates: the literal value.
	Literal interface{} `json:"literal,omitempty"`
}

// EmitSQL converts a validated Node into a dialect-neutral SQLPredicate.
// Only resource.* fields are translated to SQL column references; all other
// paths (subject.*, context.*, env.*) are omitted because they cannot be
// expressed as a pure SQL row filter — the PDP evaluates them at runtime.
//
// If the entire condition contains no resource.* predicates the returned
// predicate is nil (meaning: no SQL row filter; PDP handles the full condition).
func EmitSQL(n *Node) (*SQLPredicate, error) {
	return emitNode(n)
}

func emitNode(n *Node) (*SQLPredicate, error) {
	if n == nil {
		return nil, fmt.Errorf("dsl/sql: nil node")
	}
	if len(n.All) > 0 {
		operands := make([]SQLPredicate, 0, len(n.All))
		for _, child := range n.All {
			p, err := emitNode(child)
			if err != nil {
				return nil, err
			}
			if p != nil {
				operands = append(operands, *p)
			}
		}
		if len(operands) == 0 {
			return nil, nil
		}
		if len(operands) == 1 {
			return &operands[0], nil
		}
		return &SQLPredicate{Op: "AND", Operands: operands}, nil
	}
	if len(n.Any) > 0 {
		operands := make([]SQLPredicate, 0, len(n.Any))
		for _, child := range n.Any {
			p, err := emitNode(child)
			if err != nil {
				return nil, err
			}
			if p != nil {
				operands = append(operands, *p)
			}
		}
		if len(operands) == 0 {
			return nil, nil
		}
		if len(operands) == 1 {
			return &operands[0], nil
		}
		return &SQLPredicate{Op: "OR", Operands: operands}, nil
	}
	if n.Not != nil {
		inner, err := emitNode(n.Not)
		if err != nil {
			return nil, err
		}
		if inner == nil {
			return nil, nil
		}
		return &SQLPredicate{Op: "NOT", Operands: []SQLPredicate{*inner}}, nil
	}

	// Predicate leaf — only emit resource.* paths as SQL.
	if n.Field == "" || PathPrefix(splitPrefix(n.Field)) != PathResource {
		return nil, nil
	}
	return emitLeaf(n)
}

func emitLeaf(n *Node) (*SQLPredicate, error) {
	col := splitAttr(n.Field)
	val, err := parseLiteralForSQL(n.Value)
	if err != nil {
		return nil, fmt.Errorf("dsl/sql: field=%q: %w", n.Field, err)
	}
	sqlOp, err := mapOp(n.Op)
	if err != nil {
		return nil, err
	}
	return &SQLPredicate{
		Op:      sqlOp,
		Column:  col,
		Literal: val,
	}, nil
}

func mapOp(op Op) (string, error) {
	switch op {
	case OpEq:
		return "=", nil
	case OpNeq:
		return "<>", nil
	case OpLt:
		return "<", nil
	case OpLte:
		return "<=", nil
	case OpGt:
		return ">", nil
	case OpGte:
		return ">=", nil
	case OpIn:
		return "IN", nil
	case OpNin:
		return "NOT IN", nil
	case OpBetween:
		return "BETWEEN", nil
	case OpStartsWith:
		return "LIKE_PREFIX", nil
	case OpEndsWith:
		return "LIKE_SUFFIX", nil
	case OpContains:
		return "LIKE_CONTAINS", nil
	case OpMatches:
		return "REGEXP", nil
	default:
		return "", fmt.Errorf("dsl/sql: unknown op %q", op)
	}
}

func parseLiteralForSQL(raw json.RawMessage) (interface{}, error) {
	v, err := parseLiteral(raw)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func splitPrefix(field string) string {
	idx := 0
	for i, c := range field {
		if c == '.' {
			idx = i
			break
		}
	}
	return field[:idx]
}

func splitAttr(field string) string {
	for i, c := range field {
		if c == '.' {
			return field[i+1:]
		}
	}
	return field
}

// MarshalSQLPredicate serialises a SQLPredicate to JSON for the Calcite sidecar wire format.
func MarshalSQLPredicate(p *SQLPredicate) ([]byte, error) {
	if p == nil {
		return []byte("null"), nil
	}
	return json.Marshal(p)
}
