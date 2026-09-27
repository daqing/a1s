// Package testdb gives database-backed tests a package-private database:
// each caller gets its own database derived from A1S_TEST_DSN, created and
// migrated on first use, so parallel test packages never observe each
// other's fixtures through the global business-logic queries.
package testdb

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/daqing/airway/lib/repo"
)

// DSN returns the test database DSN for one package: the A1S_TEST_DSN base
// with the package name appended (…/a1s_test → …/a1s_test_api). The
// database is created from the base connection and migrated with the
// db/migrate up-files when it does not exist yet. An advisory lock keyed on
// the database name serializes create+migrate across concurrent test
// packages sharing one base database. Tests skip when A1S_TEST_DSN is unset.
func DSN(t *testing.T, pkg string) string {
	t.Helper()

	base := os.Getenv("A1S_TEST_DSN")
	if base == "" {
		t.Skip("A1S_TEST_DSN not set; skipping database-backed test")
	}

	name := "a1s_test_" + pkg
	dsn := replaceDBName(base, name)

	// hold the advisory lock for the whole test: an early release could
	// expose a half-migrated database to another package
	lockConn, err := repo.SetupDB(replaceDBName(base, "postgres"))
	if err != nil {
		t.Fatalf("testdb: connect for lock: %v", err)
	}
	t.Cleanup(func() {
		lockConn.Close()
	})

	if _, err := lockConn.Conn().Exec(`SELECT pg_advisory_lock(hashtext($1))`, name); err != nil {
		t.Fatalf("testdb: advisory lock %s: %v", name, err)
	}

	if !databaseExists(base, name) {
		createDatabase(base, name)
		migrate(t, dsn)
	}

	return dsn
}

func replaceDBName(base, name string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}

	u.Path = "/" + name
	return u.String()
}

func databaseExists(base, name string) bool {
	conn, err := repo.SetupDB(replaceDBName(base, "postgres"))
	if err != nil {
		return false
	}
	defer conn.Close()

	var exists bool
	if err := conn.Conn().QueryRow(
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return false
	}

	return exists
}

func createDatabase(base, name string) {
	conn, err := repo.SetupDB(replaceDBName(base, "postgres"))
	if err != nil {
		panic(fmt.Sprintf("testdb: connect for create: %v", err))
	}
	defer conn.Close()

	if _, err := conn.Conn().Exec(fmt.Sprintf(`CREATE DATABASE %s`, name)); err != nil {
		panic(fmt.Sprintf("testdb: create database %s: %v", name, err))
	}
}

// migrationsDir walks up from the working directory until it finds the
// repository's db/migrate folder (package tests run with their own package
// directory as working directory).
func migrationsDir(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("testdb: get working directory: %v", err)
	}

	for {
		candidate := filepath.Join(dir, "db", "migrate")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("testdb: db/migrate not found above %s", dir)
		}

		dir = parent
	}
}

// migrate applies the db/migrate up-files in filename order.
func migrate(t *testing.T, dsn string) {
	t.Helper()

	dir := migrationsDir(t)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("testdb: read migrations: %v", err)
	}

	var files []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)

	conn, err := repo.SetupDB(dsn)
	if err != nil {
		t.Fatalf("testdb: connect for migrate: %v", err)
	}
	defer conn.Close()

	for _, file := range files {
		sqlBytes, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatalf("testdb: read %s: %v", file, err)
		}

		if _, err := conn.Conn().Exec(string(sqlBytes)); err != nil {
			t.Fatalf("testdb: apply %s: %v", file, err)
		}
	}
}
