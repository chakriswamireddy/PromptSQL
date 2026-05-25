package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/governance-platform/pkg/auth"
)

var tracer = otel.Tracer("live-feed-broadcaster")

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 5 * time.Second,
	ReadBufferSize:   1024,
	WriteBufferSize:  4096,
	CheckOrigin: func(r *http.Request) bool {
		// Origin validated via JWT; allow all during upgrade.
		return true
	},
}

// connTracker enforces per-user connection cap.
type connTracker struct {
	mu    sync.Mutex
	counts map[string]int
	max   int
}

func newConnTracker(max int) *connTracker {
	return &connTracker{counts: make(map[string]int), max: max}
}

func (t *connTracker) acquire(userID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts[userID] >= t.max {
		return false
	}
	t.counts[userID]++
	return true
}

func (t *connTracker) release(userID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts[userID] > 0 {
		t.counts[userID]--
	}
}

type wsHandler struct {
	hub     *Hub
	cfg     config
	log     zerolog.Logger
	tracker *connTracker
}

func newWSHandler(hub *Hub, cfg config, log zerolog.Logger) *wsHandler {
	return &wsHandler{
		hub:     hub,
		cfg:     cfg,
		log:     log,
		tracker: newConnTracker(cfg.MaxConnPerUser),
	}
}

func (h *wsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "ws.upgrade")
	defer span.End()

	// 1. Extract and validate JWT from ?token= query param.
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		metricAuthErrors.Inc()
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}

	claims, err := auth.ValidateJWT(tokenStr)
	if err != nil {
		metricAuthErrors.Inc()
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	tenantID := claims.TenantID
	userID := claims.SubjectID
	span.SetAttributes(
		attribute.String("tenant_id", tenantID),
		attribute.String("user_id", userID),
	)

	// 2. Per-user connection cap.
	if !h.tracker.acquire(userID) {
		http.Error(w, "connection limit reached", http.StatusTooManyRequests)
		return
	}

	// 3. Parse filters from query params.
	q := r.URL.Query()
	riskMin, _ := strconv.ParseFloat(q.Get("risk_score_min"), 64)
	filter := ConnectionFilter{
		UserID:       q.Get("user_id"),
		Resource:     q.Get("resource"),
		Decision:     q.Get("decision"),
		RiskScoreMin: riskMin,
	}

	// 4. Upgrade.
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.tracker.release(userID)
		h.log.Error().Err(err).Msg("websocket upgrade failed")
		return
	}

	c := &conn{
		tenantID: tenantID,
		userID:   userID,
		filter:   filter,
		send:     make(chan []byte, h.cfg.DropBufferSize),
	}
	h.hub.register <- c

	go h.writePump(ctx, ws, c)
	h.readPump(ws, c, userID)
}

// readPump drains client messages (ping/pong) and detects disconnect.
func (h *wsHandler) readPump(ws *websocket.Conn, c *conn, userID string) {
	defer func() {
		h.hub.unregister <- c
		h.tracker.release(userID)
		_ = ws.Close()
	}()

	ws.SetReadLimit(512)
	_ = ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	ws.SetPongHandler(func(_ string) error {
		return ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	})

	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				h.log.Debug().Err(err).Str("user_id", userID).Msg("ws read error")
			}
			return
		}
	}
}

// writePump sends events from hub to the WebSocket client.
func (h *wsHandler) writePump(ctx context.Context, ws *websocket.Conn, c *conn) {
	ticker := time.NewTicker(h.cfg.PingInterval)
	defer func() {
		ticker.Stop()
		_ = ws.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				_ = ws.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-ctx.Done():
			return
		}
	}
}

// healthHandler is a simple liveness probe.
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// parseDecision is a helper to extract decision from resource string.
func parseDecision(s string) string {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return s
}
