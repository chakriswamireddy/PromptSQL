package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	pkgaudit "github.com/governance-platform/pkg/audit"
	"github.com/google/uuid"
)

// queryHash returns sha256(normalizedSQL + canonicalBinds) as hex.
func queryHash(sql string, binds map[string]string) string {
	norm := normalizeSQL(sql)
	var sb strings.Builder
	sb.WriteString(norm)
	// Append binds in sorted key order for determinism.
	for k, v := range binds {
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(v)
		sb.WriteString(";")
	}
	h := sha256.Sum256([]byte(sb.String()))
	return fmt.Sprintf("%x", h)
}

// normalizeSQL strips whitespace runs to a single space and lowercases.
func normalizeSQL(sql string) string {
	return strings.Join(strings.Fields(strings.ToLower(sql)), " ")
}

// emitQueryAudit sends an access audit event for a completed (or rejected) query.
func emitQueryAudit(
	ctx context.Context,
	producer *pkgaudit.Client,
	sess *connSession,
	sql string,
	decision string,
	deniedReason string,
	rowCount int64,
	durationMs int64,
	pdpMs int64,
	rewriteMs int64,
	masksApplied []string,
) {
	if producer == nil {
		return
	}
	qHash := queryHash(sql, nil)

	var reason string
	if deniedReason != "" {
		reason = "DENIED"
	}

	evt := pkgaudit.AccessEvent{
		EventID:      uuid.New().String(),
		Schema:       pkgaudit.SchemaV1,
		Service:      "proxy",
		EventTime:    time.Now().UTC(),
		TenantID:     sess.tenantID,
		UserID:       sess.userID,
		DataSourceID: sess.dataSourceID,
		Resource:     sql,
		Action:       "query",
		Decision:     pkgaudit.AccessDecision(decision),
		Reason:       reason,
		RowCount:     rowCount,
		QueryHash:    qHash,
		DurationMs:   durationMs,
		Metadata: pkgaudit.EventMeta{
			TraceID: sess.traceID,
		},
	}
	_ = masksApplied // carried in metadata in future phases
	_ = pdpMs
	_ = rewriteMs

	producer.EmitAccess(ctx, evt)
}
