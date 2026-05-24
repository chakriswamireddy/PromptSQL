package migrate_test

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	migratelib "github.com/governance-platform/pkg/migrate"
)

func TestWriteHashManifest_ContainsAllUpFiles(t *testing.T) {
	mfs := fstest.MapFS{
		"0001_extensions.up.sql": {Data: []byte("SELECT 1;")},
		"0002_roles.up.sql":      {Data: []byte("SELECT 2;")},
		"0001_extensions.down.sql": {Data: []byte("SELECT 0;")}, // should be excluded
	}

	tmp, err := os.CreateTemp(t.TempDir(), "manifest-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	if err := migratelib.WriteHashManifest(mfs, tmp.Name()); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)
	if !strings.Contains(content, "0001_extensions.up.sql") {
		t.Error("manifest missing 0001 entry")
	}
	if !strings.Contains(content, "0002_roles.up.sql") {
		t.Error("manifest missing 0002 entry")
	}
	if strings.Contains(content, "down.sql") {
		t.Error("manifest must not include .down.sql files")
	}

	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			t.Errorf("unexpected manifest line: %q", line)
			continue
		}
		if len(parts[0]) != 64 {
			t.Errorf("expected 64-char SHA-256 hash, got %d chars in %q", len(parts[0]), line)
		}
	}
}

func TestWriteHashManifest_Deterministic(t *testing.T) {
	mfs := fstest.MapFS{
		"0001_extensions.up.sql": {Data: []byte("CREATE EXTENSION pgcrypto;")},
	}

	tmp1, _ := os.CreateTemp(t.TempDir(), "m1-*.txt")
	tmp1.Close()
	tmp2, _ := os.CreateTemp(t.TempDir(), "m2-*.txt")
	tmp2.Close()

	_ = migratelib.WriteHashManifest(mfs, tmp1.Name())
	_ = migratelib.WriteHashManifest(mfs, tmp2.Name())

	d1, _ := os.ReadFile(tmp1.Name())
	d2, _ := os.ReadFile(tmp2.Name())

	if string(d1) != string(d2) {
		t.Error("WriteHashManifest is not deterministic")
	}
}
