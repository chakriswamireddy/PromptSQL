// Package differ detects schema drift between crawl runs.
package differ

import (
	"github.com/governance-platform/schema-crawler/internal/connector"
	"github.com/governance-platform/schema-crawler/internal/store"
)

// DriftKind describes what changed.
type DriftKind string

const (
	DriftNew     DriftKind = "new"
	DriftChanged DriftKind = "changed"
	DriftDropped DriftKind = "dropped"
)

// DriftEvent describes a single schema change.
type DriftEvent struct {
	Kind       DriftKind
	SchemaName string
	TableName  string
	ColumnName string
	OldType    string
	NewType    string
}

// Diff computes the difference between existing stored columns and freshly crawled columns.
// existing: columns currently stored in schema_metadata for this data source.
// fresh:    columns returned by the connector in the current crawl.
func Diff(existing []store.Column, fresh []connector.ColumnInfo) (events []DriftEvent, droppedIDs []string) {
	existingMap := make(map[string]store.Column, len(existing))
	for _, c := range existing {
		key := c.SchemaName + "." + c.TableName + "." + c.ColumnName
		existingMap[key] = c
	}

	seenKeys := make(map[string]bool, len(fresh))
	for _, f := range fresh {
		key := f.SchemaName + "." + f.TableName + "." + f.ColumnName
		seenKeys[key] = true

		ex, found := existingMap[key]
		if !found {
			events = append(events, DriftEvent{
				Kind:       DriftNew,
				SchemaName: f.SchemaName,
				TableName:  f.TableName,
				ColumnName: f.ColumnName,
				NewType:    f.DataType,
			})
			continue
		}

		if ex.DataType != f.DataType {
			events = append(events, DriftEvent{
				Kind:       DriftChanged,
				SchemaName: f.SchemaName,
				TableName:  f.TableName,
				ColumnName: f.ColumnName,
				OldType:    ex.DataType,
				NewType:    f.DataType,
			})
		}
	}

	// Detect dropped columns (present in DB but not in fresh crawl).
	for key, ex := range existingMap {
		if !seenKeys[key] && ex.DroppedAt == nil {
			events = append(events, DriftEvent{
				Kind:       DriftDropped,
				SchemaName: ex.SchemaName,
				TableName:  ex.TableName,
				ColumnName: ex.ColumnName,
				OldType:    ex.DataType,
			})
			droppedIDs = append(droppedIDs, ex.ID)
		}
	}

	return events, droppedIDs
}
