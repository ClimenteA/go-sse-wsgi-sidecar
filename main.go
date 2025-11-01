package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/golang-jwt/jwt/v5"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

var ctx = context.Background()

type SSEClient struct {
	channel chan string
}

func getRedisClient() *redis.Client {
	url := os.Getenv("GO_SSE_SIDECAR_REDIS_URL")
	if url == "" {
		log.Fatal("GO_SSE_SIDECAR_REDIS_URL not set")
	}

	opts, err := redis.ParseURL(url)
	if err != nil {
		log.Fatalf("Failed to parse Redis URL: %v", err)
	}

	return redis.NewClient(opts)
}

func subscribeToUserChannel(rdb *redis.Client, userID int64, msgChan chan<- string, ctx context.Context) {
	channelName := fmt.Sprintf("events:user:%d", userID)
	log.Printf("[SSE] Subscribing to Redis channel: %s", channelName)

	pubsub := rdb.Subscribe(ctx, channelName)
	defer pubsub.Close()

	// Wait for subscription confirmation
	_, err := pubsub.Receive(ctx)
	if err != nil {
		log.Printf("[SSE] Failed to subscribe to %s: %v", channelName, err)
		return
	}

	ch := pubsub.Channel()

	for {
		select {
		case msg := <-ch:
			if msg != nil {
				log.Printf("[SSE] User %d received message: %s", userID, msg.Payload)
				select {
				case msgChan <- msg.Payload:
				default:
					log.Printf("[SSE] Dropping message for user %d (client slow)", userID)
				}
			}
		case <-ctx.Done():
			log.Printf("[SSE] Stopping subscription for user %d", userID)
			return
		}
	}
}

type SSETokenClaims struct {
	UserID int64 `json:"user_id"`
	jwt.RegisteredClaims
}

func verifySseToken(tokenString string, secret string) (*SSETokenClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &SSETokenClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})

	if err != nil {
		return nil, fmt.Errorf("token parse error: %v", err)
	}

	if claims, ok := token.Claims.(*SSETokenClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, fmt.Errorf("invalid token")
}

func sseHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Credentials", "true")

	token := r.URL.Query().Get("ssetoken")
	secret := os.Getenv("GO_SSE_SIDECAR_TOKEN")

	claims, err := verifySseToken(token, secret)
	if err != nil {
		log.Printf("Token verification failed: %v", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	userID := claims.UserID
	log.Printf("Authenticated SSE connection for user %d (expires: %v)", userID, claims.ExpiresAt.Time)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Create per-client context that respects request cancellation
	clientCtx, cancel := context.WithCancel(r.Context())
	defer cancel()

	client := &SSEClient{
		channel: make(chan string, 10),
	}

	rdb := getRedisClient()
	go subscribeToUserChannel(rdb, userID, client.channel, clientCtx)

	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send messages to client
	for {
		select {
		case msg := <-client.channel:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-clientCtx.Done():
			log.Printf("Closing SSE for user %d", userID)
			return
		}
	}
}

func main() {
	_ = godotenv.Load()

	rdb := getRedisClient()
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis error: %v", err)
	}

	http.HandleFunc("/sse-events", sseHandler)

	port := os.Getenv("GO_SSE_SIDECAR_PORT")
	if port == "" {
		port = "5687"
	}

	log.Println("[SSE-SIDECAR] Server running on :" + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
