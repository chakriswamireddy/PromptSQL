package main

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// LiveEvent is the canonical envelope pushed to WebSocket clients.
type LiveEvent struct {
	EventID    string          `json:"event_id"`
	EventType  string          `json:"event_type"` // "access" | "policy"
	TenantID   string          `json:"tenant_id"`
	UserID     string          `json:"user_id,omitempty"`
	Resource   string          `json:"resource,omitempty"`
	Decision   string          `json:"decision,omitempty"`
	RiskScore  float64         `json:"risk_score,omitempty"`
	TraceID    string          `json:"trace_id,omitempty"`
	EventTime  time.Time       `json:"event_time"`
	Detail     json.RawMessage `json:"detail,omitempty"`
}

// ConnectionFilter describes which events a client is interested in.
type ConnectionFilter struct {
	UserID       string  // empty = all
	Resource     string  // empty = all (prefix match)
	Decision     string  // empty = all
	RiskScoreMin float64 // 0 = all
}

func (f ConnectionFilter) matches(ev LiveEvent) bool {
	if f.UserID != "" && ev.UserID != f.UserID {
		return false
	}
	if f.Resource != "" && !strings.HasPrefix(ev.Resource, f.Resource) {
		return false
	}
	if f.Decision != "" && ev.Decision != f.Decision {
		return false
	}
	if f.RiskScoreMin > 0 && ev.RiskScore < f.RiskScoreMin {
		return false
	}
	return true
}

// conn represents a single authenticated WebSocket client.
type conn struct {
	tenantID string
	userID   string
	filter   ConnectionFilter
	send     chan []byte // bounded; drop oldest on overflow
}

// Hub fans events out to registered connections, isolated per tenant.
type Hub struct {
	mu    sync.RWMutex
	rooms map[string]map[*conn]struct{} // tenantID → set of *conn

	register   chan *conn
	unregister chan *conn
	broadcast  chan LiveEvent

	dropBufferSize int
}

func newHub(dropBufSize int) *Hub {
	return &Hub{
		rooms:          make(map[string]map[*conn]struct{}),
		register:       make(chan *conn, 64),
		unregister:     make(chan *conn, 64),
		broadcast:      make(chan LiveEvent, 4096),
		dropBufferSize: dropBufSize,
	}
}

func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			if h.rooms[c.tenantID] == nil {
				h.rooms[c.tenantID] = make(map[*conn]struct{})
			}
			h.rooms[c.tenantID][c] = struct{}{}
			h.mu.Unlock()
			metricActiveConns.WithLabelValues(c.tenantID).Inc()

		case c := <-h.unregister:
			h.mu.Lock()
			if conns, ok := h.rooms[c.tenantID]; ok {
				delete(conns, c)
				if len(conns) == 0 {
					delete(h.rooms, c.tenantID)
				}
			}
			h.mu.Unlock()
			close(c.send)
			metricActiveConns.WithLabelValues(c.tenantID).Dec()

		case ev := <-h.broadcast:
			payload, _ := json.Marshal(ev)
			h.mu.RLock()
			conns := h.rooms[ev.TenantID]
			h.mu.RUnlock()

			for c := range conns {
				if !c.filter.matches(ev) {
					continue
				}
				select {
				case c.send <- payload:
					metricBroadcast.WithLabelValues(ev.TenantID, ev.EventType).Inc()
				default:
					// Buffer full — drop oldest by draining one then re-sending.
					select {
					case <-c.send:
					default:
					}
					select {
					case c.send <- payload:
					default:
					}
					metricDropped.WithLabelValues(ev.TenantID).Inc()
				}
			}
		}
	}
}

func (h *Hub) Broadcast(ev LiveEvent) {
	select {
	case h.broadcast <- ev:
	default:
		// Hub channel full — drop.
		metricDropped.WithLabelValues(ev.TenantID).Inc()
	}
}
