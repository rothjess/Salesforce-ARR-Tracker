package main

import (
	"log"
	"net/http"
	"os"

	"github.com/coder/arr-tracker-sf/api"
	"github.com/coder/arr-tracker-sf/internal/db"
	"github.com/coder/arr-tracker-sf/internal/salesforce"
)

func main() {
	// --- Config ---
	sfClientID := mustEnv("SF_CLIENT_ID")
	sfClientSecret := mustEnv("SF_CLIENT_SECRET")
	sfCallbackURL := mustEnv("SF_CALLBACK_URL")
	sfInstanceURL := envOr("SF_INSTANCE_URL", "https://login.salesforce.com")
	sessionSecret := mustEnv("SESSION_SECRET")
	dbURL := mustEnv("DATABASE_URL")
	port := envOr("PORT", "8080")

	// --- Database ---
	database, err := db.New(dbURL)
	if err != nil {
		log.Fatalf("FATAL: could not connect to database: %v", err)
	}
	if err := database.Migrate(); err != nil {
		log.Fatalf("FATAL: migration failed: %v", err)
	}
	log.Println("Database connected and migrated")

	// --- Salesforce config (no tokens yet — users log in via browser) ---
	sfCfg := salesforce.Config{
		ClientID:     sfClientID,
		ClientSecret: sfClientSecret,
		CallbackURL:  sfCallbackURL,
		InstanceURL:  sfInstanceURL,
	}

	// --- API handler ---
	handler := api.New(database, sfCfg, sessionSecret)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// --- Start server ---
	addr := ":" + port
	log.Printf("Server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("FATAL: server error: %v", err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("FATAL: required environment variable %s is not set", key)
	}
	return v
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
