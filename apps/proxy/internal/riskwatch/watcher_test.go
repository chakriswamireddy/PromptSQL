package riskwatch_test

import (
	"testing"

	"github.com/governance-platform/proxy/internal/riskwatch"
)

func TestActionFromScore(t *testing.T) {
	tests := []struct {
		score  int
		action riskwatch.Action
	}{
		{score: 30, action: riskwatch.ActionNone},
		{score: 70, action: riskwatch.ActionNone},
		{score: 85, action: riskwatch.ActionNone},
		{score: 86, action: riskwatch.ActionMask},
		{score: 95, action: riskwatch.ActionMask},
		{score: 96, action: riskwatch.ActionTerminate},
		{score: 100, action: riskwatch.ActionTerminate},
	}
	for _, tt := range tests {
		got := riskwatch.ActionForScore(tt.score)
		if got != tt.action {
			t.Errorf("ActionForScore(%d) = %v, want %v", tt.score, got, tt.action)
		}
	}
}

func TestSubscribeUnsubscribe(t *testing.T) {
	w := riskwatch.New(nil) // nil redis — only subscribe/unsubscribe logic tested
	ch, unsub := w.Subscribe("tenant1", "user1")
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	unsub()
	// Second unsubscribe must not panic.
	unsub()
}
