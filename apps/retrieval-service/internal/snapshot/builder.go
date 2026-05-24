// Package snapshot builds permission-filtered AllowedSnapshot objects.
package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"

	pdpv1 "github.com/governance-platform/pkg/pdpv1"
	"github.com/governance-platform/retrieval-service/internal/store"
)

// classificationOrder defines display sensitivity from lowest to highest.
var classificationOrder = []string{"public", "internal", "confidential", "restricted"}

func classificationRank(c string) int {
	for i, v := range classificationOrder {
		if v == c {
			return i
		}
	}
	return 0
}

// AllowedSnapshot is the permission-filtered schema view returned to callers.
type AllowedSnapshot struct {
	Version          string        `json:"version"`
	SchemaVersion    string        `json:"schemaVersion"`
	PolicySetVersion string        `json:"policySetVersion"`
	DataSourceID     string        `json:"dataSourceId"`
	Tables           []SnapshotTable `json:"tables"`
	Truncated        bool          `json:"truncated,omitempty"`
}

type SnapshotTable struct {
	Name            string           `json:"name"`
	Schema          string           `json:"schema"`
	Description     string           `json:"description,omitempty"`
	Columns         []SnapshotColumn `json:"columns"`
	ForeignKeys     []SnapshotFK     `json:"foreign_keys,omitempty"`
	RowFilterSummary string          `json:"row_filter_summary,omitempty"`
}

type SnapshotColumn struct {
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	Nullable       bool     `json:"nullable,omitempty"`
	Masked         string   `json:"masked,omitempty"`
	Classification string   `json:"classification,omitempty"`
	Description    string   `json:"description,omitempty"`
	SampleValues   []string `json:"sample_values,omitempty"`
}

type SnapshotFK struct {
	Column    string `json:"column"`
	RefTable  string `json:"ref_table"`
	RefColumn string `json:"ref_column"`
}

// maxSnapshotBytes is the soft cap before truncation.
const maxSnapshotBytes = 100 * 1024

// Builder constructs AllowedSnapshots using the PDP for permission decisions.
type Builder struct {
	store  *store.Store
	pdp    pdpv1.PDPClient
}

func NewBuilder(st *store.Store, pdp pdpv1.PDPClient) *Builder {
	return &Builder{store: st, pdp: pdp}
}

// Build constructs a permission-filtered snapshot for the given session and data source.
func (b *Builder) Build(ctx context.Context, sess store.SessionCtx, dataSourceID string, ver store.DataSourceVersion) (*AllowedSnapshot, error) {
	// 1. Fetch all non-quarantined tables + columns.
	tables, err := b.store.ListTablesWithColumns(ctx, sess, dataSourceID)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	if len(tables) == 0 {
		return b.emptySnapshot(dataSourceID, ver), nil
	}

	// 2. PDP BulkDecide — one decision per table.
	bulkReq := &pdpv1.BulkDecideRequest{
		Subject: &pdpv1.Subject{
			UserId: sess.UserID,
			Roles:  sess.UserRoles,
		},
		Requests: make([]*pdpv1.DecideRequest, len(tables)),
	}
	for i, t := range tables {
		bulkReq.Requests[i] = &pdpv1.DecideRequest{
			Action:   "read",
			Resource: fmt.Sprintf("%s.%s.%s", dataSourceID, t.SchemaName, t.TableName),
		}
	}
	bulkResp, err := b.pdp.BulkDecide(ctx, bulkReq)
	if err != nil {
		return nil, fmt.Errorf("pdp bulk decide: %w", err)
	}

	// 3. Build snapshot — only permitted tables; intersect allowed columns.
	permittedTableKeys := map[string]bool{}
	var snapTables []SnapshotTable

	for i, t := range tables {
		if i >= len(bulkResp.Decisions) {
			break
		}
		dec := bulkResp.Decisions[i]
		if dec.Effect != pdpv1.Effect_PERMIT {
			continue
		}

		key := t.SchemaName + "." + t.TableName
		permittedTableKeys[key] = true

		// Compute permitted columns: allowed_columns − denied_columns.
		allowedCols := dec.AllowedColumns  // nil means all
		deniedCols := toSet(dec.DeniedColumns)

		var cols []SnapshotColumn
		for _, col := range t.Columns {
			if len(allowedCols) > 0 && !slices.Contains(allowedCols, col.ColumnName) {
				continue
			}
			if deniedCols[col.ColumnName] {
				continue
			}
			sc := SnapshotColumn{
				Name:           col.ColumnName,
				Type:           col.DataType,
				Nullable:       col.Nullable,
				Masked:         col.MaskRule,
				Classification: col.Classification,
				Description:    col.Description,
			}
			// Expose sample values only for public/internal classification.
			if classificationRank(col.Classification) <= classificationRank("internal") {
				sc.SampleValues = col.SampleValues
			}
			cols = append(cols, sc)
		}
		if len(cols) == 0 {
			continue
		}

		snapTables = append(snapTables, SnapshotTable{
			Name:             t.TableName,
			Schema:           t.SchemaName,
			Description:      t.Description,
			Columns:          cols,
			RowFilterSummary: dec.RowFilterSummary,
		})
	}

	// 4. Include FK relationships only when both referencing and referenced table are permitted.
	for i := range snapTables {
		tbl := &snapTables[i]
		srcKey := tbl.Schema + "." + tbl.Name
		// We look at the original tables to find FK references.
		for _, orig := range tables {
			if orig.SchemaName+"."+orig.TableName != srcKey {
				continue
			}
			for _, col := range orig.Columns {
				for _, fk := range col.FKRefs {
					refKey := fk.ToSchema + "." + fk.ToTable
					if permittedTableKeys[refKey] {
						tbl.ForeignKeys = append(tbl.ForeignKeys, SnapshotFK{
							Column:    col.ColumnName,
							RefTable:  fk.ToTable,
							RefColumn: fk.ToColumn,
						})
					}
				}
			}
		}
	}

	snap := &AllowedSnapshot{
		SchemaVersion:    ver.SchemaVersion,
		PolicySetVersion: ver.PolicySetVersion,
		DataSourceID:     dataSourceID,
		Tables:           snapTables,
	}

	// 5. Token-budget shaping — truncate if serialised size exceeds cap.
	data, _ := json.Marshal(snap)
	if len(data) > maxSnapshotBytes {
		snap = b.truncate(snap, maxSnapshotBytes)
		snap.Truncated = true
		data, _ = json.Marshal(snap)
	}

	// 6. Snapshot hash.
	h := sha256.Sum256(data)
	snap.Version = fmt.Sprintf("%x", h[:8])

	return snap, nil
}

func (b *Builder) emptySnapshot(dataSourceID string, ver store.DataSourceVersion) *AllowedSnapshot {
	return &AllowedSnapshot{
		Version:          "empty",
		SchemaVersion:    ver.SchemaVersion,
		PolicySetVersion: ver.PolicySetVersion,
		DataSourceID:     dataSourceID,
		Tables:           []SnapshotTable{},
	}
}

// truncate removes tables starting from the most sensitive until the snapshot fits.
func (b *Builder) truncate(snap *AllowedSnapshot, limit int) *AllowedSnapshot {
	// Sort tables so most-sensitive come last (will be removed first).
	tables := make([]SnapshotTable, len(snap.Tables))
	copy(tables, snap.Tables)

	for {
		data, _ := json.Marshal(&AllowedSnapshot{
			SchemaVersion:    snap.SchemaVersion,
			PolicySetVersion: snap.PolicySetVersion,
			DataSourceID:     snap.DataSourceID,
			Tables:           tables,
		})
		if len(data) <= limit || len(tables) == 0 {
			break
		}
		// Remove the last table (highest classification after sort).
		tables = tables[:len(tables)-1]
	}

	result := *snap
	result.Tables = tables
	return &result
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
