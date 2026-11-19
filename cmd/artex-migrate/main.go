package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	pgdb "github.com/Autumn-27/artex/db"
)

// artex-migrate applies the idempotent ARTEX schema to one explicitly named
// PostgreSQL database. It refuses to run without an expected-database guard.
func main() {
	expected := strings.TrimSpace(os.Getenv("ARTEX_EXPECT_DB"))
	if expected == "" {
		log.Fatal("ARTEX_EXPECT_DB is required")
	}
	dsn, _, err := pgdb.DSN()
	if err != nil {
		log.Fatal(err)
	}
	db, err := pgdb.Open(dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	var current string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&current); err != nil {
		log.Fatal(err)
	}
	if current != expected {
		log.Fatalf("refusing migration: current_database=%q expected=%q", current, expected)
	}
	fmt.Printf("schema_ready database=%s\n", current)
}
