package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/dkolancs/crashlab/demo/internal/kit"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	deadline := time.Now().Add(40 * time.Second)
	for {
		if err := db.Ping(); err == nil {
			break
		} else if time.Now().After(deadline) {
			log.Fatal(err)
		}
		time.Sleep(time.Second)
	}

	stats := &kit.Stats{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stock", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		defer cancel()
		var qty int
		err := db.QueryRowContext(ctx, "select qty from stock where sku = $1", "widget").Scan(&qty)
		stats.Record(err != nil, time.Since(start))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"sku": "widget", "qty": qty})
	})
	kit.Serve(stats, mux)
}
