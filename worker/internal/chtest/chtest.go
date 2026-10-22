// Package chtest gives tests a fresh ClickHouse database with PSG's DDL
// applied. It needs PSG_TEST_CLICKHOUSE_URL (e.g. http://127.0.0.1:8123);
// without it the test is skipped.
package chtest

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// DB creates database name (dropping any old one), applies every
// control/clickhouse/*.sql file in order, and returns the server URL.
func DB(t *testing.T, name string) string {
	t.Helper()
	u := os.Getenv("PSG_TEST_CLICKHOUSE_URL")
	if u == "" {
		t.Skip("PSG_TEST_CLICKHOUSE_URL not set")
	}
	Query(t, u, "", "DROP DATABASE IF EXISTS "+name)
	Query(t, u, "", "CREATE DATABASE "+name)
	t.Cleanup(func() { Query(t, u, "", "DROP DATABASE IF EXISTS "+name) })

	_, here, _, _ := runtime.Caller(0)
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(here), "../../../control/clickhouse/*.sql"))
	if len(files) == 0 {
		t.Fatal("no ClickHouse DDL found")
	}
	comments := regexp.MustCompile(`--[^\n]*`)
	for _, f := range files {
		ddl, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range strings.Split(comments.ReplaceAllString(string(ddl), ""), ";") {
			if strings.TrimSpace(stmt) != "" {
				Query(t, u, name, stmt)
			}
		}
	}
	return u
}

// Query runs q and returns its TSV output, trimmed.
func Query(t *testing.T, chURL, db, q string) string {
	t.Helper()
	u := chURL + "/"
	if db != "" {
		u += "?" + url.Values{"database": {db}}.Encode()
	}
	resp, err := http.Post(u, "text/plain", strings.NewReader(q))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", q, body)
	}
	return strings.TrimSpace(string(body))
}
