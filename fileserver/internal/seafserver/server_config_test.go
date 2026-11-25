package seafserver

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestConfigFileStringEscapes(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
		valid bool
	}{
		{`one\stwo\\three`, `one two\three`, true},
		{`one\q`, "", false},
		{`one\`, "", false},
	} {
		value := configValue{text: test.input, found: true}
		if got, valid := value.string(); got != test.want || valid != test.valid {
			t.Errorf("INI string %q: got (%q, %v), want (%q, %v)", test.input, got, valid, test.want, test.valid)
		}
	}
}

func TestServerConfigNullAndDuplicateOverrides(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "seafile.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const schema = "CREATE TABLE SeafileConf (cfg_group TEXT, cfg_key TEXT, value TEXT, property INTEGER);" +
		"INSERT INTO SeafileConf VALUES ('example', 'setting', NULL, 7), ('example', 'setting', 'ignored', 8);"
	if _, err := db.ExecContext(ctx, schema); err != nil {
		t.Fatal(err)
	}
	config := NewServerConfig(db, []byte("[example]\nsetting = file default\n"))
	if value, ok := config.String(ctx, "example", "setting"); !ok || value != "file default" {
		t.Fatalf("first matching NULL should fall back to the file: %q, %v", value, ok)
	}
	writer, err := OpenSQLiteReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := writeConfig(ctx, writer, "example", "setting", "updated"); err != nil {
		t.Fatal(err)
	}
	if value, ok := config.String(ctx, "example", "setting"); !ok || value != "updated" {
		t.Fatalf("database override = %q, %v", value, ok)
	}
	var count, properties int
	const query = "SELECT COUNT(*), SUM(property) FROM SeafileConf WHERE value = 'updated'"
	if err := db.QueryRowContext(ctx, query).Scan(&count, &properties); err != nil || count != 2 || properties != 15 {
		t.Fatalf("duplicate rows and properties after update = %d, %d, %v", count, properties, err)
	}
}
