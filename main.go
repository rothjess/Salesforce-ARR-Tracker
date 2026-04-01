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
	// --- Config from environment ---
	sfClientID := mustEnv("SF_CLIENT_ID")
	sfClientSecret := mustEnv("SF_CLIENT_SECRET")
	sfUsername := mustEnv("SF_USERNAME")
	sfPassword := mustEnv("SF_PASSWORD")
	sfSecurityToken := envOr("SF_SECURITY_TOKEN", "")
	sfInstanceURL := envOr("SF_INSTANCE_URL", "https://login.salesforce.com")

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

	// --- Salesforce client ---
	sf := salesforce.New(salesforce.Config{
		ClientID:     sfClientID,
		ClientSecret: sfClientSecret,
		Username:     sfUsername,
		Password:     sfPassword + sfSecurityToken,
		InstanceURL:  sfInstanceURL,
	})

	// --- API handler ---
	handler := api.New(database, sf)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// --- Background scheduler (24hr auto-sync) ---
	handler.StartScheduler()

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
