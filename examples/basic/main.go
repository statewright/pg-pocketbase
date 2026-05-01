package main

import (
	"log"
	"os"

	"github.com/statewright/pg-pocketbase/pgpb"
)

func main() {
	pgURL := os.Getenv("POSTGRES_URL")
	if pgURL == "" {
		pgURL = "postgres://pgpb:pgpb@localhost:5432?sslmode=disable"
	}

	app := pgpb.NewWithPostgres(pgURL)

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
