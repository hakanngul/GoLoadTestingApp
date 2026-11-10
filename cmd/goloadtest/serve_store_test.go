package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestParseStoreSpec(t *testing.T) {
	cases := []struct {
		in, kind, loc string
	}{
		{"memory", "memory", ""},
		{"MEMORY", "memory", ""},
		{defaultDB, "sqlite", "./goload.db"},
		{"sqlite:/data/goload.db", "sqlite", "/data/goload.db"},
		{"sqlite:///data/goload.db", "sqlite", "/data/goload.db"},
		{"sqlite::memory:", "sqlite", ":memory:"},
		{"./x.db", "sqlite", "./x.db"},
		{"/var/lib/goload/state.sqlite3", "sqlite", "/var/lib/goload/state.sqlite3"},
		{"postgres://u:p@db:5432/goload?sslmode=disable", "postgres", "postgres://u:p@db:5432/goload?sslmode=disable"},
		{"postgresql://db/goload", "postgres", "postgresql://db/goload"},
		{"host=db dbname=goload user=u", "postgres", "host=db dbname=goload user=u"},
	}
	for _, c := range cases {
		got, err := parseStoreSpec(c.in)
		if err != nil {
			t.Errorf("parseStoreSpec(%q): %v", c.in, err)
			continue
		}
		if got.kind != c.kind || got.loc != c.loc {
			t.Errorf("parseStoreSpec(%q) = %+v, want %s %q", c.in, got, c.kind, c.loc)
		}
	}
	for _, bad := range []string{"", "sqlite:", "mysql://x", "goload"} {
		if _, err := parseStoreSpec(bad); err == nil {
			t.Errorf("parseStoreSpec(%q) accepted", bad)
		}
	}
	// Credentials never reach the log line.
	pg, _ := parseStoreSpec("postgres://u:secret@db:5432/goload")
	if s := pg.String(); s != "postgres db:5432/goload" {
		t.Errorf("postgres String() = %q", s)
	}
}

func TestOpenStoreSQLiteCreatesDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "goload.db")
	spec, err := parseStoreSpec("sqlite:" + path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := openStore(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
}
