package connectors

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// ─── SQL result stream ────────────────────────────────────────────────────────

// sqlResultStream wraps *sql.Rows and implements ResultStream.
// Used by MySQL, SQL Server, Oracle, Snowflake (JDBC-compat), and Databricks.
type sqlResultStream struct {
	rows    *sql.Rows
	cols    []string
	maxRows int
	count   int
	err     error
}

func newSQLResultStream(rows *sql.Rows, maxRows int) (*sqlResultStream, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("result stream columns: %w", err)
	}
	return &sqlResultStream{rows: rows, cols: cols, maxRows: maxRows}, nil
}

func (s *sqlResultStream) Next() bool {
	if s.err != nil {
		return false
	}
	if s.maxRows > 0 && s.count >= s.maxRows {
		return false
	}
	if !s.rows.Next() {
		return false
	}
	s.count++
	return true
}

func (s *sqlResultStream) Scan(dest ...any) error {
	return s.rows.Scan(dest...)
}

func (s *sqlResultStream) Columns() []string { return s.cols }

func (s *sqlResultStream) Err() error {
	if s.err != nil {
		return s.err
	}
	return s.rows.Err()
}

func (s *sqlResultStream) Close() error { return s.rows.Close() }

// ─── MongoDB result stream ────────────────────────────────────────────────────

// mongoResultStream wraps a mongo.Cursor and emits one JSON-serialised document
// per row.  The single "column" is named "_doc" and contains a JSON string.
// This lets the PEP and crawler consume MongoDB output via the same ResultStream
// contract without requiring callers to know the wire format.
type mongoResultStream struct {
	cursor  *mongo.Cursor
	maxRows int
	count   int
	current []byte // JSON-encoded current document
	err     error
}

func newMongoResultStream(cursor *mongo.Cursor, maxRows int) *mongoResultStream {
	return &mongoResultStream{cursor: cursor, maxRows: maxRows}
}

func (m *mongoResultStream) Next() bool {
	if m.err != nil {
		return false
	}
	if m.maxRows > 0 && m.count >= m.maxRows {
		return false
	}
	// Use context.Background() since we cannot carry caller context here.
	if !m.cursor.Next(context.Background()) {
		return false
	}
	var doc bson.M
	if err := m.cursor.Decode(&doc); err != nil {
		m.err = fmt.Errorf("mongo decode: %w", err)
		return false
	}
	b, err := json.Marshal(doc)
	if err != nil {
		m.err = fmt.Errorf("mongo marshal: %w", err)
		return false
	}
	m.current = b
	m.count++
	return true
}

func (m *mongoResultStream) Scan(dest ...any) error {
	if len(dest) < 1 {
		return fmt.Errorf("mongo result stream: Scan requires at least 1 destination")
	}
	switch v := dest[0].(type) {
	case *string:
		*v = string(m.current)
	case *[]byte:
		*v = append((*v)[:0], m.current...)
	default:
		return fmt.Errorf("mongo result stream: Scan dest must be *string or *[]byte, got %T", dest[0])
	}
	return nil
}

func (m *mongoResultStream) Columns() []string { return []string{"_doc"} }

func (m *mongoResultStream) Err() error {
	if m.err != nil {
		return m.err
	}
	return m.cursor.Err()
}

func (m *mongoResultStream) Close() error { return m.cursor.Close(context.Background()) }

// ─── BigQuery result stream ───────────────────────────────────────────────────

// bqRow is a generic BigQuery row (map of column → value).
type bqRow = map[string]bigQueryValue

// bigQueryValue is the interface that the BigQuery Go client returns for each
// cell; we use a thin wrapper so the result stream does not import the BQ SDK
// in this file (the actual type assertion happens in bigquery.go).
type bigQueryValue interface{}

// bqResultStream implements ResultStream over a slice of pre-fetched BQ rows.
// BigQuery's RowIterator is consumed eagerly in Execute (up to MaxRows) and
// stored in this struct to avoid pulling the heavy BQ client into the stream
// interface.
type bqResultStream struct {
	rows    []bqRow
	cols    []string
	idx     int
	err     error
}

func newBQResultStream(rows []bqRow, cols []string) *bqResultStream {
	return &bqResultStream{rows: rows, cols: cols, idx: -1}
}

func (b *bqResultStream) Next() bool {
	b.idx++
	return b.idx < len(b.rows)
}

func (b *bqResultStream) Scan(dest ...any) error {
	if b.idx < 0 || b.idx >= len(b.rows) {
		return fmt.Errorf("bq result stream: Scan called out of bounds (idx=%d)", b.idx)
	}
	row := b.rows[b.idx]
	for i, col := range b.cols {
		if i >= len(dest) {
			break
		}
		val, ok := row[col]
		if !ok {
			continue
		}
		switch v := dest[i].(type) {
		case *any:
			*v = val
		case *string:
			if val == nil {
				*v = ""
			} else {
				*v = fmt.Sprintf("%v", val)
			}
		case *[]byte:
			if val == nil {
				*v = nil
			} else {
				*v = []byte(fmt.Sprintf("%v", val))
			}
		}
	}
	return nil
}

func (b *bqResultStream) Columns() []string { return b.cols }
func (b *bqResultStream) Err() error        { return b.err }
func (b *bqResultStream) Close() error      { return nil }
