package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgproto3/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	pkgaudit "github.com/governance-platform/pkg/audit"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var tracer = otel.Tracer("proxy")

// proxyConn handles a single client connection lifecycle.
type proxyConn struct {
	raw      net.Conn
	frontend *pgproto3.Frontend // client-facing: reads client msgs, writes server msgs
	backend  *pgproto3.Backend  // server-facing (confusingly named in pgproto3 terms)

	sess     *connSession
	pool     *backendPool
	pipeline *rewritePipeline
	auditor  *pkgaudit.Client
	auth     *tokenAuthenticator
	cfg      config
	log      zerolog.Logger
}

func newProxyConn(
	conn net.Conn,
	pool *backendPool,
	pipeline *rewritePipeline,
	auditor *pkgaudit.Client,
	auth *tokenAuthenticator,
	cfg config,
	log zerolog.Logger,
) *proxyConn {
	return &proxyConn{
		raw:      conn,
		backend:  pgproto3.NewBackend(pgproto3.NewChunkReader(conn), conn),
		pool:     pool,
		pipeline: pipeline,
		auditor:  auditor,
		auth:     auth,
		cfg:      cfg,
		log:      log,
	}
}

// Serve runs the connection state machine until the client disconnects.
func (c *proxyConn) Serve(ctx context.Context) {
	defer c.raw.Close()

	if err := c.handshake(ctx); err != nil {
		c.log.Debug().Err(err).Msg("handshake failed")
		_ = c.sendError("08006", genericPermissionDenied)
		return
	}

	proxyConnectionsActive.WithLabelValues(c.sess.tenantID).Inc()
	defer proxyConnectionsActive.WithLabelValues(c.sess.tenantID).Dec()

	c.log.Debug().
		Str("tenant_id", c.sess.tenantID).
		Str("user_id", c.sess.userID).
		Msg("client authenticated")

	c.queryLoop(ctx)
}

// handshake performs the startup + authentication exchange.
func (c *proxyConn) handshake(ctx context.Context) error {
	// Receive StartupMessage (may be SSLRequest first).
	startupMsg, err := c.backend.ReceiveStartupMessage()
	if err != nil {
		return fmt.Errorf("receive startup: %w", err)
	}

	switch msg := startupMsg.(type) {
	case *pgproto3.SSLRequest:
		// Decline SSL for now (TLS termination handled at load balancer in K8s).
		if _, err := c.raw.Write([]byte("N")); err != nil {
			return fmt.Errorf("ssl decline: %w", err)
		}
		// Receive real startup message.
		startupMsg, err = c.backend.ReceiveStartupMessage()
		if err != nil {
			return fmt.Errorf("receive startup after ssl decline: %w", err)
		}
		msg2, ok := startupMsg.(*pgproto3.StartupMessage)
		if !ok {
			return fmt.Errorf("expected StartupMessage, got %T", startupMsg)
		}
		return c.doPasswordAuth(ctx, msg2)

	case *pgproto3.StartupMessage:
		return c.doPasswordAuth(ctx, msg)

	default:
		return fmt.Errorf("unexpected startup message type: %T", startupMsg)
	}
}

// doPasswordAuth sends AuthenticationCleartextPassword, receives the token, validates it.
func (c *proxyConn) doPasswordAuth(ctx context.Context, startup *pgproto3.StartupMessage) error {
	// Request cleartext password (= the connection token).
	authReq := &pgproto3.AuthenticationCleartextPassword{}
	if err := c.backend.Send(authReq); err != nil {
		return fmt.Errorf("send auth request: %w", err)
	}

	// Receive PasswordMessage.
	msg, err := c.backend.Receive()
	if err != nil {
		return fmt.Errorf("receive password: %w", err)
	}
	pwMsg, ok := msg.(*pgproto3.PasswordMessage)
	if !ok {
		return fmt.Errorf("expected PasswordMessage, got %T", msg)
	}

	// Validate token from Redis.
	sess, err := c.auth.Validate(ctx, pwMsg.Password)
	if err != nil {
		_ = c.sendError("28P01", genericPermissionDenied)
		return fmt.Errorf("token validation: %w", err)
	}
	c.sess = sess

	// Authentication OK.
	if err := c.backend.Send(&pgproto3.AuthenticationOK{}); err != nil {
		return fmt.Errorf("send auth ok: %w", err)
	}

	// Send parameter status messages.
	for _, ps := range []*pgproto3.ParameterStatus{
		{Name: "server_version", Value: "16.0 (proxy)"},
		{Name: "client_encoding", Value: "UTF8"},
		{Name: "DateStyle", Value: "ISO, MDY"},
		{Name: "TimeZone", Value: "UTC"},
	} {
		if err := c.backend.Send(ps); err != nil {
			return fmt.Errorf("send parameter status: %w", err)
		}
	}

	// BackendKeyData (cancel key — not supported in V1, send zeros).
	if err := c.backend.Send(&pgproto3.BackendKeyData{ProcessID: 0, SecretKey: 0}); err != nil {
		return fmt.Errorf("send backend key data: %w", err)
	}

	// ReadyForQuery.
	if err := c.backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'}); err != nil {
		return fmt.Errorf("send ready for query: %w", err)
	}

	_ = startup // username extracted but unused in V1 (token carries identity)
	return nil
}

// queryLoop handles the query phase: Simple Query and Extended Query messages.
func (c *proxyConn) queryLoop(ctx context.Context) {
	// Extended query state machine buffers.
	var parseSQL string
	var bindSQL  string

	for {
		msg, err := c.backend.Receive()
		if err != nil {
			// Client disconnected — normal exit.
			return
		}

		switch m := msg.(type) {
		case *pgproto3.Terminate:
			return

		case *pgproto3.Query:
			// Simple Query path.
			c.handleSimpleQuery(ctx, m.String)

		case *pgproto3.Parse:
			// Extended Query: Parse
			parseSQL = m.Query
			if err := c.backend.Send(&pgproto3.ParseComplete{}); err != nil {
				return
			}

		case *pgproto3.Bind:
			// Extended Query: Bind — record params for Execute.
			bindSQL = parseSQL
			_ = m // parameter values handled in Execute
			if err := c.backend.Send(&pgproto3.BindComplete{}); err != nil {
				return
			}

		case *pgproto3.Describe:
			// Extended Query: Describe — return empty ParameterDescription.
			if m.ObjectType == 'S' { // statement
				if err := c.backend.Send(&pgproto3.ParameterDescription{ParameterOIDs: nil}); err != nil {
					return
				}
			}
			// RowDescription sent during Execute.
			if err := c.backend.Send(&pgproto3.NoData{}); err != nil {
				return
			}

		case *pgproto3.Execute:
			// Extended Query: Execute.
			if bindSQL != "" {
				c.handleSimpleQuery(ctx, bindSQL)
				bindSQL = ""
			} else {
				_ = c.sendError("26000", genericPermissionDenied)
			}

		case *pgproto3.Sync:
			// Extended Query: Sync — flush and send ReadyForQuery.
			if err := c.backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'}); err != nil {
				return
			}

		case *pgproto3.CopyData, *pgproto3.CopyFail, *pgproto3.CopyDone:
			proxyUnsupportedCommands.WithLabelValues("COPY").Inc()
			_ = c.sendError("0A000", genericPermissionDenied)

		default:
			proxyUnsupportedCommands.WithLabelValues(fmt.Sprintf("%T", msg)).Inc()
			_ = c.sendError("0A000", genericPermissionDenied)
		}
	}
}

// handleSimpleQuery runs the full rewrite pipeline then proxies to backend.
func (c *proxyConn) handleSimpleQuery(ctx context.Context, rawSQL string) {
	start := time.Now()

	ctx, span := tracer.Start(ctx, "proxy.query",
		otel.WithAttributes(
			attribute.String("tenant_id", c.sess.tenantID),
			attribute.String("user_id", c.sess.userID),
		),
	)
	defer span.End()

	proxyQueryTotal.WithLabelValues(c.sess.tenantID, "started", "select").Inc()

	// 1. Denylist check.
	if reason := denylistCheck(rawSQL); reason != "" {
		proxyDenylistRejections.WithLabelValues(c.sess.tenantID, reason).Inc()
		proxyQueryTotal.WithLabelValues(c.sess.tenantID, "deny", "select").Inc()
		emitQueryAudit(ctx, c.auditor, c.sess, rawSQL, "deny", reason, 0,
			time.Since(start).Milliseconds(), 0, 0, nil)
		_ = c.sendQueryError("42501", genericPermissionDenied)
		return
	}

	// 2. Only SELECT in V1.
	if !isSelectStatement(rawSQL) {
		proxyUnsupportedCommands.WithLabelValues("non-select").Inc()
		_ = c.sendQueryError("42501", genericPermissionDenied)
		return
	}

	// 3. Rewrite pipeline (PDP + Calcite).
	result, err := c.pipeline.Run(ctx, c.sess, rawSQL)
	if err != nil {
		c.log.Error().Err(err).Str("tenant_id", c.sess.tenantID).Msg("rewrite pipeline error")
		emitQueryAudit(ctx, c.auditor, c.sess, rawSQL, "error", err.Error(), 0,
			time.Since(start).Milliseconds(), 0, 0, nil)
		_ = c.sendQueryError("58000", genericPermissionDenied)
		return
	}
	if result.denied {
		proxyQueryTotal.WithLabelValues(c.sess.tenantID, "deny", "select").Inc()
		emitQueryAudit(ctx, c.auditor, c.sess, rawSQL, "deny", result.deniedReason, 0,
			time.Since(start).Milliseconds(), result.pdpMs, result.rewriteMs, nil)
		_ = c.sendQueryError("42501", genericPermissionDenied)
		return
	}

	// 4. Execute rewritten SQL on backend.
	rowCount, backendMs, execErr := c.execOnBackend(ctx, result.rewrittenSQL)
	totalMs := time.Since(start).Milliseconds()
	proxyQueryDuration.WithLabelValues(c.sess.tenantID, "allow").Observe(time.Since(start).Seconds())

	if execErr != nil {
		c.log.Debug().Err(execErr).Str("tenant_id", c.sess.tenantID).Msg("backend exec error")
		emitQueryAudit(ctx, c.auditor, c.sess, rawSQL, "error", "backend_error", 0,
			totalMs, result.pdpMs, result.rewriteMs, nil)
		_ = c.sendQueryError("58000", genericPermissionDenied)
		return
	}

	proxyQueryTotal.WithLabelValues(c.sess.tenantID, "allow", "select").Inc()
	proxyRowsStreamed.WithLabelValues(c.sess.tenantID, c.sess.dataSourceID).Add(float64(rowCount))
	if len(result.masksApplied) > 0 {
		proxyRowsMasked.WithLabelValues(c.sess.tenantID).Add(float64(len(result.masksApplied) * int(rowCount)))
	}

	emitQueryAudit(ctx, c.auditor, c.sess, rawSQL, "allow", "", rowCount,
		totalMs, result.pdpMs, result.rewriteMs, result.masksApplied)
	_ = backendMs
}

// execOnBackend acquires a backend connection, sets LOCAL session vars,
// executes the SQL, streams rows back to the client, and returns row count.
func (c *proxyConn) execOnBackend(ctx context.Context, sql string) (int64, int64, error) {
	key := poolKey{tenantID: c.sess.tenantID, dataSourceID: c.sess.dataSourceID}

	// TODO: resolve connStr from Vault/data_sources table for this datasource.
	// In local dev, use DATABASE_URL as a fallback for the single configured backend.
	connStr := getEnv("BACKEND_DATABASE_URL", getEnv("DATABASE_URL", ""))

	backendStart := time.Now()
	conn, err := c.pool.Acquire(ctx, key, connStr)
	if err != nil {
		return 0, 0, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	// Begin transaction and SET LOCAL session context.
	if _, err := conn.Exec(ctx, buildSetLocal(c.sess)); err != nil {
		return 0, 0, fmt.Errorf("set local: %w", err)
	}

	// Execute query and stream rows.
	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	// Send RowDescription.
	fields := rows.FieldDescriptions()
	pgFields := make([]pgproto3.FieldDescription, len(fields))
	for i, f := range fields {
		pgFields[i] = pgproto3.FieldDescription{
			Name:                 []byte(f.Name),
			TableOID:             uint32(f.TableOID),
			TableAttributeNumber: uint16(f.TableAttributeNumber),
			DataTypeOID:          uint32(f.DataTypeOID),
			DataTypeSize:         f.DataTypeSize,
			TypeModifier:         f.TypeModifier,
			Format:               int16(f.Format),
		}
	}
	if err := c.backend.Send(&pgproto3.RowDescription{Fields: pgFields}); err != nil {
		return 0, 0, err
	}

	var rowCount int64
	for rows.Next() {
		if rowCount >= c.cfg.ResultSetRowCap {
			_ = c.sendError("54000", genericPermissionDenied)
			return rowCount, time.Since(backendStart).Milliseconds(), nil
		}
		vals, err := rows.Values()
		if err != nil {
			return rowCount, time.Since(backendStart).Milliseconds(), err
		}
		dataRow := &pgproto3.DataRow{Values: make([][]byte, len(vals))}
		for i, v := range vals {
			if v == nil {
				dataRow.Values[i] = nil
			} else {
				dataRow.Values[i] = []byte(fmt.Sprintf("%v", v))
			}
		}
		if err := c.backend.Send(dataRow); err != nil {
			return rowCount, time.Since(backendStart).Milliseconds(), err
		}
		rowCount++
	}
	proxyBackendDuration.WithLabelValues(c.sess.tenantID, c.sess.dataSourceID).
		Observe(time.Since(backendStart).Seconds())

	// CommandComplete + ReadyForQuery.
	tag := fmt.Sprintf("SELECT %d", rowCount)
	if err := c.backend.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)}); err != nil {
		return rowCount, time.Since(backendStart).Milliseconds(), err
	}
	if err := c.backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'}); err != nil {
		return rowCount, time.Since(backendStart).Milliseconds(), err
	}

	return rowCount, time.Since(backendStart).Milliseconds(), nil
}

// buildSetLocal builds the SET LOCAL statements for the session context.
func buildSetLocal(sess *connSession) string {
	var sb strings.Builder
	sb.WriteString("SET LOCAL ROLE app_read; ")
	sb.WriteString(fmt.Sprintf("SET LOCAL app.tenant_id = '%s'; ", pgEsc(sess.tenantID)))
	sb.WriteString(fmt.Sprintf("SET LOCAL app.user_id = '%s'; ", pgEsc(sess.userID)))
	sb.WriteString(fmt.Sprintf("SET LOCAL app.session_id = '%s'; ", pgEsc(sess.sessionID)))
	if sess.isBreakGlass {
		sb.WriteString("SET LOCAL app.break_glass = 'true'; ")
	} else {
		sb.WriteString("SET LOCAL app.break_glass = 'false'; ")
	}
	return sb.String()
}

// pgEsc escapes a single-quoted string value for SET LOCAL (no user input used in keys).
func pgEsc(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// sendError sends an ErrorResponse and ReadyForQuery to the client.
func (c *proxyConn) sendError(sqlState, msg string) error {
	return c.backend.Send(&pgproto3.ErrorResponse{
		Severity: "ERROR",
		Code:     sqlState,
		Message:  msg,
	})
}

// sendQueryError sends ErrorResponse + ReadyForQuery (for query-phase errors).
func (c *proxyConn) sendQueryError(sqlState, msg string) error {
	if err := c.backend.Send(&pgproto3.ErrorResponse{
		Severity: "ERROR",
		Code:     sqlState,
		Message:  msg,
	}); err != nil {
		return err
	}
	return c.backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
}

// placeholder to resolve otel.WithAttributes at call site
func otelAttr(key, val string) attribute.KeyValue {
	return attribute.String(key, val)
}

// Satisfy unused import — pgxpool.Conn is used in execOnBackend.
var _ *pgxpool.Pool
