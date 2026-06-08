package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"goagent/agent"
	"goagent/config"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	// --- CLI flags ---
	testPrompt := flag.String("test-prompt", "", "Send a single prompt to the agent and exit")
	showConfig := flag.Bool("show-config", false, "Print the current config and exit")
	flag.Parse()

	// Load config
	cfg := config.Load()
	if *showConfig {
		fmt.Println(cfg)
		os.Exit(0)
	}

	// Create agent
	ag := agent.New(cfg)

	// Create voice manager for WebSocket audio ingestion
	voiceMgr := agent.NewVoiceManager(cfg, ag)

	// If --test-prompt is set, run once and exit
	if *testPrompt != "" {
		answer, err := ag.Run(context.Background(), "", *testPrompt)
		if err != nil {
			log.Fatalf("Error: %v", err)
		}
		fmt.Println(answer)
		os.Exit(0)
	}

	// --- HTTP server ---
	mux := http.NewServeMux()

	// GET /health
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// POST /chat
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			SessionID string `json:"session_id"`
			Prompt    string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		if req.Prompt == "" {
			http.Error(w, `{"error":"prompt is required"}`, http.StatusBadRequest)
			return
		}
		result, err := ag.RunJSON(r.Context(), req.SessionID, req.Prompt)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(result)
	})

	// POST /reset
	mux.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			SessionID string `json:"session_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		if req.SessionID == "" {
			http.Error(w, `{"error":"session_id is required"}`, http.StatusBadRequest)
			return
		}
		ag.ResetSession(req.SessionID)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"session reset"}`))
	})

	// --- WebSocket endpoint for ESP32 audio ---
	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			return true // ESP32 is not a browser
		},
	}

	mux.HandleFunc("/audio", func(w http.ResponseWriter, r *http.Request) {
		// Validate auth token before upgrading
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + cfg.VoiceAgentToken
		if cfg.VoiceAgentToken != "" && auth != expected {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[ws] Upgrade failed: %v", err)
			return
		}
		defer ws.Close()

		log.Printf("[ws] ESP32 connected from %s", r.RemoteAddr)

		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("[ws] Read error: %v", err)
				}
				break
			}
			if mt == websocket.BinaryMessage {
				voiceMgr.ProcessBatch(data)
			}
			// Ignore text frames and other types
		}

		log.Printf("[ws] ESP32 disconnected from %s", r.RemoteAddr)
	})

	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
	}

	// Clean up sessions older than 1 week, every 24 hours
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			ag.CleanOldSessions(7 * 24 * time.Hour)
		}
	}()

	// --- Graceful shutdown ---
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	log.Printf("GoAgent listening on %s", cfg.ListenAddr)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
	log.Println("Server stopped.")
}
