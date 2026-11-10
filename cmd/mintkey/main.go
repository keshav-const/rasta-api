// Command mintkey creates an API key, stores only its hash, and prints the RAW key
// ONCE to stdout. A dev/ops tool until the M3 portal issues keys self-serve.
//
//	go run ./cmd/mintkey -plan pro -name "demo" [-email you@example.com]
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keshav-const/rasta-api/internal/auth"
	"github.com/keshav-const/rasta-api/internal/config"
	"github.com/keshav-const/rasta-api/internal/store"
)

func main() {
	plan := flag.String("plan", "free", "free | pro | enterprise")
	name := flag.String("name", "", "human label for the key")
	email := flag.String("email", "", "owner email (optional)")
	flag.Parse()

	if p, ok := auth.Plans[*plan]; !ok || p.Name == "anonymous" {
		log.Fatalf("invalid plan %q (use free | pro | enterprise)", *plan)
	}

	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db pool: %v", err)
	}
	defer pool.Close()
	db := store.New(pool)

	// 24 random bytes -> "khoj_<48 hex>". Printed once; only the hash is persisted.
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		log.Fatal(err)
	}
	raw := "khoj_" + hex.EncodeToString(buf)
	prefix := raw[:14]

	id, err := db.InsertAPIKey(context.Background(), *name, *plan, auth.HashKey(raw), prefix, *email)
	if err != nil {
		log.Fatalf("insert key: %v", err)
	}

	// Raw key to stdout (capture it); metadata to stderr.
	fmt.Fprintln(os.Stderr, "API key created — copy it now, it is not shown again:")
	fmt.Println(raw)
	fmt.Fprintf(os.Stderr, "id=%s  plan=%s  name=%q\n", id, *plan, *name)
}
