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
var clients = make(map[*SSEClient]bool)
var addClient = make(chan *SSEClient)
var removeClient = make(chan *SSEClient)
var broadcast = make(chan string)

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

func redisSubscriber(rdb *redis.Client) {
	log.Println("[SSE-SIDECAR] Subscribing to channel: events")

	pubsub := rdb.Subscribe(ctx, "events")
	defer func() {
		log.Println("[SSE-SIDECAR] Closing subscription to channel: events")
		pubsub.Close()
	}()

	_, err := pubsub.Receive(ctx)
	if err != nil {
		log.Printf("[SSE-SIDECAR] Failed to subscribe: %v\n", err)
		return
	}

	ch := pubsub.Channel()

	log.Println("[SSE-SIDECAR] Listening for messages...")

	for msg := range ch {
		log.Printf("[SSE-SIDECAR] Received message: %s\n", msg.Payload)
		broadcast <- msg.Payload
	}

	log.Println("[SSE-SIDECAR] Subscription channel closed")
}

func clientManager() {
	for {
		select {
		case client := <-addClient:
			clients[client] = true
		case client := <-removeClient:
			delete(clients, client)
			close(client.channel)
		case msg := <-broadcast:
			for client := range clients {
				select {
				case client.channel <- msg:
				default:
					delete(clients, client)
					close(client.channel)
				}
			}
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
		fmt.Println("Token verification failed:", err)
		return
	}

	fmt.Printf("Token success! UserID: %d\n", claims.UserID)
	fmt.Printf("Token expires at: %v\n", claims.ExpiresAt.Time)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	client := &SSEClient{channel: make(chan string, 10)}
	addClient <- client
	defer func() { removeClient <- client }()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	for {
		select {
		case msg := <-client.channel:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
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

	go redisSubscriber(rdb)
	go clientManager()

	http.HandleFunc("/sse-events", sseHandler)

	port := os.Getenv("GO_SSE_SIDECAR_PORT")
	if port == "" {
		port = "5687"
	}

	log.Println("[SSE-SIDECAR] Server running on :" + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
