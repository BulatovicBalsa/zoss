package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Println("=== Ordering Service (Race Condition + Webhook Canonicalization Demo) ===")

	cassandraHost := envOrDefault("CASSANDRA_HOST", "localhost")
	cassandraTimeout := 120 * time.Second
	redisAddr := envOrDefault("REDIS_ADDR", "localhost:6379")
	webhookSecret := envOrDefault("WEBHOOK_SECRET", "default-webhook-secret-change-me")

	maxDelayMs, _ := strconv.Atoi(envOrDefault("MAX_PROCESSING_DELAY_MS", "2000"))
	maxProcessingDelay := time.Duration(maxDelayMs) * time.Millisecond

	lockTTLMs, _ := strconv.Atoi(envOrDefault("LOCK_TTL_MS", "1000"))
	lockTTL := time.Duration(lockTTLMs) * time.Millisecond

	listenAddr := envOrDefault("LISTEN_ADDR", ":8080")

	log.Printf("[main] Connecting to Cassandra at %s (timeout %v)...", cassandraHost, cassandraTimeout)
	session, err := ConnectCassandra(cassandraHost, cassandraTimeout)
	if err != nil {
		log.Fatalf("[main] Failed to connect to Cassandra: %v", err)
	}
	defer session.Close()

	log.Printf("[main] Connecting to Redis at %s...", redisAddr)
	rdb := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("[main] Failed to connect to Redis: %v", err)
	}
	log.Println("[main] Connected to Redis")

	store := NewOrderStore(session)
	if err := store.InitSchema(); err != nil {
		log.Fatalf("[main] Failed to initialize schema: %v", err)
	}

	sm := NewStateMachine(store, rdb, lockTTL, maxProcessingDelay)

	log.Printf("[main] lockTTL=%v, maxProcessingDelay=%v", lockTTL, maxProcessingDelay)

	log.Printf("[main] webhookSecret configured (%d chars)", len(webhookSecret))

	h := NewHandlers(store, sm, webhookSecret)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Post("/orders", h.CreateOrder)
	r.Get("/orders/{orderID}", h.GetOrder)
	r.Post("/orders/{orderID}/pay", h.PayOrder)
	r.Post("/orders/{orderID}/cancel", h.CancelOrder)
	r.Post("/orders/{orderID}/ship", h.ShipOrder)
	r.Get("/orders/{orderID}/history", h.GetOrderHistory)

	// Shipping webhook endpoint (receives status updates from logistics provider)
	r.Post("/webhooks/shipping", h.ShippingWebhook)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	log.Printf("[main] Listening on %s", listenAddr)
	if err := http.ListenAndServe(listenAddr, r); err != nil {
		log.Fatalf("[main] Server error: %v", err)
	}
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}