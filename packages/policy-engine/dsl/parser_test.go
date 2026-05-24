package dsl_test

import (
	"encoding/json"
	"testing"

	"github.com/governance-platform/policy-engine/dsl"
)

func TestParse_ValidPredicate(t *testing.T) {
	raw := []byte(`{"field":"subject.department","op":"eq","value":"finance"}`)
	n, err := dsl.Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !n.IsPredicate() {
		t.Fatal("expected predicate node")
	}
	if n.Field != "subject.department" {
		t.Errorf("got field %q", n.Field)
	}
}

func TestParse_ValidAll(t *testing.T) {
	raw := []byte(`{"all":[{"field":"subject.department","op":"eq","value":"finance"},{"field":"resource.classification","op":"eq","value":"internal"}]}`)
	n, err := dsl.Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !n.IsCombinator() {
		t.Fatal("expected combinator node")
	}
	if len(n.All) != 2 {
		t.Errorf("expected 2 children, got %d", len(n.All))
	}
}

func TestParse_Empty(t *testing.T) {
	_, err := dsl.Parse(nil)
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestParse_InvalidJSON(t *testing.T) {
	_, err := dsl.Parse([]byte(`{invalid}`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestValidate_MaxDepth(t *testing.T) {
	// Build a deeply nested node exceeding MaxDepth.
	node := &dsl.Node{Field: "subject.x", Op: dsl.OpEq, Value: json.RawMessage(`"v"`)}
	for i := 0; i < dsl.MaxDepth+1; i++ {
		node = &dsl.Node{Not: node}
	}
	if err := dsl.Validate(node); err == nil {
		t.Fatal("expected depth error")
	}
}

func TestValidate_MaxNodes(t *testing.T) {
	children := make([]*dsl.Node, dsl.MaxNodes+1)
	for i := range children {
		children[i] = &dsl.Node{Field: "subject.x", Op: dsl.OpEq, Value: json.RawMessage(`"v"`)}
	}
	node := &dsl.Node{All: children}
	if err := dsl.Validate(node); err == nil {
		t.Fatal("expected node count error")
	}
}

func TestValidate_UnknownOp(t *testing.T) {
	n := &dsl.Node{Field: "subject.x", Op: "badop", Value: json.RawMessage(`"v"`)}
	if err := dsl.Validate(n); err == nil {
		t.Fatal("expected unknown op error")
	}
}

func TestValidate_UnknownFieldPrefix(t *testing.T) {
	n := &dsl.Node{Field: "user.x", Op: dsl.OpEq, Value: json.RawMessage(`"v"`)}
	if err := dsl.Validate(n); err == nil {
		t.Fatal("expected unknown prefix error")
	}
}

func TestValidate_TemplateInterpolation(t *testing.T) {
	n := &dsl.Node{Field: "subject.x", Op: dsl.OpEq, Value: json.RawMessage(`"${inject}"`)}
	if err := dsl.Validate(n); err == nil {
		t.Fatal("expected template interpolation error")
	}
}

func TestValidate_RegexReDoS(t *testing.T) {
	// Pattern that is valid RE2 — just ensure very long patterns are rejected.
	longPattern := make([]byte, dsl.MaxRegexPatternLen+1)
	for i := range longPattern {
		longPattern[i] = 'a'
	}
	raw, _ := json.Marshal(string(longPattern))
	n := &dsl.Node{Field: "subject.x", Op: dsl.OpMatches, Value: json.RawMessage(raw)}
	if err := dsl.Validate(n); err == nil {
		t.Fatal("expected pattern-too-long error")
	}
}

func TestValidate_Between_WrongArity(t *testing.T) {
	n := &dsl.Node{Field: "subject.age", Op: dsl.OpBetween, Value: json.RawMessage(`[18]`)}
	if err := dsl.Validate(n); err == nil {
		t.Fatal("expected between arity error")
	}
}

func TestValidate_Valid(t *testing.T) {
	raw := []byte(`{"all":[{"field":"subject.department","op":"eq","value":"finance"},{"field":"resource.classification","op":"in","value":["internal","restricted"]}]}`)
	n, err := dsl.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := dsl.Validate(n); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

// FuzzParse exercises the parser with arbitrary bytes; it must never panic.
func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"field":"subject.x","op":"eq","value":"v"}`))
	f.Add([]byte(`{"all":[]}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := dsl.Parse(data)
		if err != nil {
			return
		}
		_ = dsl.Validate(n)
	})
}

// FuzzCompile exercises the compiler; it must never panic.
func FuzzCompile(f *testing.F) {
	f.Add([]byte(`{"field":"subject.x","op":"eq","value":"v"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := dsl.Parse(data)
		if err != nil {
			return
		}
		if err := dsl.Validate(n); err != nil {
			return
		}
		fn, err := dsl.Compile(n)
		if err != nil {
			return
		}
		ctx := dsl.EvalContext{
			Subject:  map[string]interface{}{"x": "v"},
			Resource: map[string]interface{}{},
			Context:  map[string]string{},
			Env:      map[string]string{},
		}
		fn(ctx, false)
	})
}
