// Package config loads runtime configuration from the environment.
// In development it also reads a local .env file (best-effort); in production
// (Render) the same variables are set directly on the service, so .env is absent.
package config

import (
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Port        string // HTTP port to listen on
	DatabaseURL string // Supabase Postgres (use the IPv4 session pooler URL)
	// Upstash Redis REST creds for rate limiting (+ caching later). When empty, the
	// limiter is disabled (fail-open) so local dev works without Redis.
	RedisRestURL   string
	RedisRestToken string
}

func Load() Config {
	_ = godotenv.Load() // ignore error: fine when there's no .env (prod)
	return Config{
		Port:           getenv("PORT", "8080"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		RedisRestURL:   os.Getenv("REDIS_REST_URL"),
		RedisRestToken: os.Getenv("REDIS_REST_TOKEN"),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
