package dsl

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// EvalContext is the runtime context passed to every compiled closure.
// It is constructed once per Decide call with zero heap allocations on the hot path.
type EvalContext struct {
	// Subject attributes resolved server-side (from SessionContext).
	Subject map[string]interface{}
	// Resource attributes (e.g. classification, owner — from data_classifications).
	Resource map[string]interface{}
	// Caller-supplied context attributes (row-level attrs known at call time).
	Context map[string]string
	// Environment attributes (server time, etc.).
	Env map[string]string
}

// ConditionFn is a compiled closure for a condition AST node.
// Returns (matched bool, trace []string). trace is populated only when explainMode=true.
type ConditionFn func(ctx EvalContext, explainMode bool) (bool, []string)

// Compile compiles a validated Node into a ConditionFn.
// The resulting closure has no reflection on the hot path (only at compile time).
func Compile(n *Node) (ConditionFn, error) {
	return compileNode(n)
}

func compileNode(n *Node) (ConditionFn, error) {
	if n == nil {
		return nil, fmt.Errorf("dsl/compile: nil node")
	}
	// Combinator: all
	if len(n.All) > 0 {
		subs := make([]ConditionFn, len(n.All))
		for i, child := range n.All {
			fn, err := compileNode(child)
			if err != nil {
				return nil, err
			}
			subs[i] = fn
		}
		return func(ctx EvalContext, explain bool) (bool, []string) {
			var trace []string
			for _, sub := range subs {
				ok, t := sub(ctx, explain)
				if explain {
					trace = append(trace, t...)
				}
				if !ok {
					if explain {
						trace = append(trace, "all: short-circuit false")
					}
					return false, trace
				}
			}
			return true, trace
		}, nil
	}
	// Combinator: any
	if len(n.Any) > 0 {
		subs := make([]ConditionFn, len(n.Any))
		for i, child := range n.Any {
			fn, err := compileNode(child)
			if err != nil {
				return nil, err
			}
			subs[i] = fn
		}
		return func(ctx EvalContext, explain bool) (bool, []string) {
			var trace []string
			for _, sub := range subs {
				ok, t := sub(ctx, explain)
				if explain {
					trace = append(trace, t...)
				}
				if ok {
					if explain {
						trace = append(trace, "any: short-circuit true")
					}
					return true, trace
				}
			}
			return false, trace
		}, nil
	}
	// Combinator: not
	if n.Not != nil {
		inner, err := compileNode(n.Not)
		if err != nil {
			return nil, err
		}
		return func(ctx EvalContext, explain bool) (bool, []string) {
			ok, trace := inner(ctx, explain)
			result := !ok
			if explain {
				trace = append(trace, fmt.Sprintf("not: %v → %v", ok, result))
			}
			return result, trace
		}, nil
	}

	// Predicate leaf
	return compilePredicate(n)
}

func compilePredicate(n *Node) (ConditionFn, error) {
	resolver, err := compileResolver(n.Field)
	if err != nil {
		return nil, err
	}
	// Parse the expected value at compile time (no allocation per call).
	compareFn, err := compileComparison(n.Op, n.Value)
	if err != nil {
		return nil, fmt.Errorf("dsl/compile: field=%q op=%q: %w", n.Field, n.Op, err)
	}
	field := n.Field
	op := n.Op
	return func(ctx EvalContext, explain bool) (bool, []string) {
		actual := resolver(ctx)
		result := compareFn(actual)
		if explain {
			return result, []string{fmt.Sprintf("field=%q op=%q actual=%v result=%v", field, op, actual, result)}
		}
		return result, nil
	}, nil
}

// compileResolver returns a function that extracts the field value from EvalContext.
func compileResolver(field string) (func(EvalContext) interface{}, error) {
	parts := strings.SplitN(field, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("dsl/compile: invalid field path %q", field)
	}
	prefix, attr := PathPrefix(parts[0]), parts[1]
	switch prefix {
	case PathSubject:
		return func(ctx EvalContext) interface{} {
			return ctx.Subject[attr]
		}, nil
	case PathResource:
		return func(ctx EvalContext) interface{} {
			return ctx.Resource[attr]
		}, nil
	case PathContext:
		return func(ctx EvalContext) interface{} {
			return ctx.Context[attr]
		}, nil
	case PathEnv:
		return func(ctx EvalContext) interface{} {
			return ctx.Env[attr]
		}, nil
	default:
		return nil, fmt.Errorf("dsl/compile: unknown prefix %q", prefix)
	}
}

// compileComparison returns a comparison function for the given op and pre-parsed value.
func compileComparison(op Op, raw json.RawMessage) (func(interface{}) bool, error) {
	switch op {
	case OpEq:
		expected, err := parseLiteral(raw)
		if err != nil {
			return nil, err
		}
		return func(actual interface{}) bool { return deepEqual(actual, expected) }, nil

	case OpNeq:
		expected, err := parseLiteral(raw)
		if err != nil {
			return nil, err
		}
		return func(actual interface{}) bool { return !deepEqual(actual, expected) }, nil

	case OpLt:
		expected, err := parseLiteral(raw)
		if err != nil {
			return nil, err
		}
		return func(actual interface{}) bool { r, ok := compareOrdered(actual, expected); return ok && r < 0 }, nil

	case OpLte:
		expected, err := parseLiteral(raw)
		if err != nil {
			return nil, err
		}
		return func(actual interface{}) bool { r, ok := compareOrdered(actual, expected); return ok && r <= 0 }, nil

	case OpGt:
		expected, err := parseLiteral(raw)
		if err != nil {
			return nil, err
		}
		return func(actual interface{}) bool { r, ok := compareOrdered(actual, expected); return ok && r > 0 }, nil

	case OpGte:
		expected, err := parseLiteral(raw)
		if err != nil {
			return nil, err
		}
		return func(actual interface{}) bool { r, ok := compareOrdered(actual, expected); return ok && r >= 0 }, nil

	case OpIn:
		var rawArr []json.RawMessage
		if err := json.Unmarshal(raw, &rawArr); err != nil {
			return nil, err
		}
		set := make([]interface{}, len(rawArr))
		for i, r := range rawArr {
			v, err := parseLiteral(r)
			if err != nil {
				return nil, err
			}
			set[i] = v
		}
		return func(actual interface{}) bool {
			for _, v := range set {
				if deepEqual(actual, v) {
					return true
				}
			}
			return false
		}, nil

	case OpNin:
		var rawArr []json.RawMessage
		if err := json.Unmarshal(raw, &rawArr); err != nil {
			return nil, err
		}
		set := make([]interface{}, len(rawArr))
		for i, r := range rawArr {
			v, err := parseLiteral(r)
			if err != nil {
				return nil, err
			}
			set[i] = v
		}
		return func(actual interface{}) bool {
			for _, v := range set {
				if deepEqual(actual, v) {
					return false
				}
			}
			return true
		}, nil

	case OpBetween:
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, err
		}
		lo, err := parseLiteral(arr[0])
		if err != nil {
			return nil, err
		}
		hi, err := parseLiteral(arr[1])
		if err != nil {
			return nil, err
		}
		return func(actual interface{}) bool {
			rlo, okLo := compareOrdered(actual, lo)
			rhi, okHi := compareOrdered(actual, hi)
			return okLo && okHi && rlo >= 0 && rhi <= 0
		}, nil

	case OpStartsWith:
		var pattern string
		if err := json.Unmarshal(raw, &pattern); err != nil {
			return nil, err
		}
		return func(actual interface{}) bool {
			s, ok := actual.(string)
			return ok && strings.HasPrefix(s, pattern)
		}, nil

	case OpEndsWith:
		var pattern string
		if err := json.Unmarshal(raw, &pattern); err != nil {
			return nil, err
		}
		return func(actual interface{}) bool {
			s, ok := actual.(string)
			return ok && strings.HasSuffix(s, pattern)
		}, nil

	case OpContains:
		var substr string
		if err := json.Unmarshal(raw, &substr); err != nil {
			return nil, err
		}
		return func(actual interface{}) bool {
			s, ok := actual.(string)
			return ok && strings.Contains(s, substr)
		}, nil

	case OpMatches:
		var pattern string
		if err := json.Unmarshal(raw, &pattern); err != nil {
			return nil, err
		}
		re, err := regexp.Compile(pattern) // RE2-compatible via Go stdlib
		if err != nil {
			return nil, fmt.Errorf("dsl/compile: regex compile %q: %w", pattern, err)
		}
		return func(actual interface{}) bool {
			s, ok := actual.(string)
			if !ok {
				return false
			}
			// Enforce input length cap to prevent ReDoS even with RE2.
			if len(s) > MaxRegexInputLen {
				s = s[:MaxRegexInputLen]
			}
			return re.MatchString(s)
		}, nil

	default:
		return nil, fmt.Errorf("dsl/compile: unknown op %q", op)
	}
}

// parseLiteral decodes a JSON literal into a Go value (string, float64, bool, time.Time, nil).
func parseLiteral(raw json.RawMessage) (interface{}, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty literal")
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		// Attempt ISO-8601 date parse.
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, nil
		}
		return s, nil
	case 't', 'f':
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, err
		}
		return b, nil
	case 'n':
		return nil, nil
	default:
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		return f, nil
	}
}

// deepEqual compares two interface{} values using reflect.DeepEqual for non-numeric types
// and numeric equality for float64 pairs.
func deepEqual(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	af, aOk := toFloat(a)
	bf, bOk := toFloat(b)
	if aOk && bOk {
		return af == bf
	}
	return reflect.DeepEqual(a, b)
}

// compareOrdered returns -1, 0, 1 for a < b, a == b, a > b.
// Returns (0, false) for incompatible types.
func compareOrdered(a, b interface{}) (int, bool) {
	// Numeric
	af, aOk := toFloat(a)
	bf, bOk := toFloat(b)
	if aOk && bOk {
		switch {
		case af < bf:
			return -1, true
		case af > bf:
			return 1, true
		default:
			return 0, true
		}
	}
	// String
	as, aStr := a.(string)
	bs, bStr := b.(string)
	if aStr && bStr {
		return strings.Compare(as, bs), true
	}
	// time.Time
	at, aT := a.(time.Time)
	bt, bT := b.(time.Time)
	if aT && bT {
		switch {
		case at.Before(bt):
			return -1, true
		case at.After(bt):
			return 1, true
		default:
			return 0, true
		}
	}
	return 0, false
}

func toFloat(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}
