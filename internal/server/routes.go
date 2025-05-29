package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"rclone_uploader/internal/errors"
	"rclone_uploader/internal/uploader"
	"time"

	"github.com/gorilla/websocket"
)

// FileInfo represents a file's metadata
type FileInfo struct {
	Name       string `json:"name"`
	UploadedAt string `json:"uploaded_at"`
}

// createContext is a helper function that creates a new context with a timeout
// This function is added to ensure the context package is directly used
func createContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

func (s *Server) RegisterRoutes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", s.healthHandler)
	mux.HandleFunc("/websocket", s.websocketHandler)
	mux.HandleFunc("/files", s.filesHandler)
	mux.HandleFunc("/", s.indexHandler)
	mux.HandleFunc("/reset", s.resetHandler)
	mux.HandleFunc("/metrics", s.metricsHandler)
	mux.HandleFunc("/metrics/reset", s.resetMetricsHandler)
	mux.HandleFunc("/queue", s.queueHandler)

	return s.corsMiddleware(mux)
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get allowed origins from environment or use default
		allowedOrigins := os.Getenv("ALLOWED_ORIGINS")
		if allowedOrigins == "" {
			// In production, this should be restricted to specific origins
			if os.Getenv("APP_ENV") == "production" {
				log.Println("Warning: Using wildcard CORS in production environment")
			}
			allowedOrigins = "*"
		}

		w.Header().Set("Access-Control-Allow-Origin", allowedOrigins)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-CSRF-Token")
		w.Header().Set("Access-Control-Allow-Credentials", "false")

		// Add security headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	// Use the createContext function to create a context with timeout
	ctx, cancel := createContext(5 * time.Second)
	defer cancel()

	// Use the context (in a real scenario, you would pass this to database operations)
	_ = ctx

	resp, err := json.Marshal(s.db.Health())
	if err != nil {
		errors.SendError(w, errors.NewError(errors.ErrorTypeSystem, "Failed to marshal health check response", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(resp); err != nil {
		log.Printf("Failed to write response: %v", err)
		errors.SendError(w, errors.NewError(errors.ErrorTypeSystem, "Failed to write response", err))
		return
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for now
	},
}

// websocketHandler handles WebSocket connections for real-time updates
func (s *Server) websocketHandler(w http.ResponseWriter, r *http.Request) {
	socket, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		errors.SendError(w, errors.NewError(errors.ErrorTypeSystem, "Failed to open websocket", err))
		return
	}

	defer func(socket *websocket.Conn) {
		if err := socket.Close(); err != nil {
			log.Printf("Failed to close websocket: %v", err)
		}
	}(socket)

	// Set read handler to discard messages and detect disconnections
	socket.SetReadDeadline(time.Time{}) // No deadline
	socket.SetPongHandler(func(string) error { return nil })

	// Start a goroutine to handle incoming messages
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := socket.ReadMessage(); err != nil {
				return // Exit if read fails
			}
		}
	}()

	// Get context from request for cancellation
	ctx := r.Context()

	// Get timezone from environment or use default
	timezone := os.Getenv("TIMEZONE")
	if timezone == "" {
		timezone = "Local" // Use local timezone as default
	}

	// Load the timezone
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.UTC
	}

	// Set ping handler to keep connection alive
	socket.SetPingHandler(func(message string) error {
		err := socket.WriteControl(websocket.PongMessage, []byte(message), time.Now().Add(time.Second))
		if err == websocket.ErrCloseSent {
			return nil
		}
		return err
	})

	// Start periodic pinger to keep connection alive
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Main event loop
	for {
		select {
		case <-ctx.Done():
			return
		case <-readDone:
			return
		case <-ticker.C:
			// Send ping to keep connection alive
			if err := socket.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(time.Second)); err != nil {
				return
			}
		case message, ok := <-uploader.RcloneOutputChan:
			if !ok {
				return
			}

			// Format message with timestamp
			currentTime := time.Now().In(location).Format("2006-01-02 15:04:05")
			messageWithTime := fmt.Sprintf("%s - %s", currentTime, message)

			// Send message to client
			if err := socket.WriteMessage(websocket.TextMessage, []byte(messageWithTime)); err != nil {
				return
			}
		case _, ok := <-uploader.ResetChan:
			if !ok {
				return
			}
			// Send reset message to client
			if err := socket.WriteMessage(websocket.TextMessage, []byte("reset")); err != nil {
				return
			}
		case file, ok := <-uploader.NewFileChan:
			if !ok {
				log.Println("NewFileChan closed")
				return
			}
			// Send new file notification to client
			if err := socket.WriteMessage(websocket.TextMessage, []byte("new_file:"+file)); err != nil {
				log.Printf("Failed to write new file message: %v", err)
				return
			}
		}
	}
}

func (s *Server) indexHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFiles("internal/static/index.html")
	if err != nil {
		http.Error(w, "Failed to parse template", http.StatusInternalServerError)
		return
	}
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, "Failed to execute template", http.StatusInternalServerError)
	}
}

func (s *Server) filesHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	files, err := s.db.GetUploadedFiles(ctx)
	if err != nil {
		http.Error(w, "Failed to fetch files", http.StatusInternalServerError)
		return
	}

	var fileInfos []FileInfo
	for _, file := range files {
		uploadedAt, ok := file["uploaded_at"].(time.Time)
		if !ok {
			continue
		}
		name, ok := file["name"].(string)
		if !ok {
			continue
		}
		fileInfos = append(fileInfos, FileInfo{
			Name:       name,
			UploadedAt: uploadedAt.Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fileInfos); err != nil {
		http.Error(w, "Failed to encode files", http.StatusInternalServerError)
		return
	}
}

func (s *Server) resetHandler(w http.ResponseWriter, r *http.Request) {
	// Accept both GET and POST methods
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	if err := s.db.Reset(ctx); err != nil {
		log.Printf("Error resetting database: %v", err)
		http.Error(w, "Failed to reset database", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	// Send reset notification to WebSocket clients
	select {
	case uploader.ResetChan <- struct{}{}:
	default:
	}

	// Send success response
	response := map[string]string{"status": "success", "message": "Database reset successfully"}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Failed to encode reset response: %v", err)
	}
}

func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	metrics := uploader.GetMetrics()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(metrics); err != nil {
		http.Error(w, "Failed to encode metrics", http.StatusInternalServerError)
		return
	}
}

func (s *Server) queueHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	queueStatus := uploader.GetUploadQueueStatus()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(queueStatus); err != nil {
		http.Error(w, "Failed to encode queue status", http.StatusInternalServerError)
		return
	}
}

func (s *Server) resetMetricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	uploader.ResetMetrics()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	response := map[string]string{"status": "success", "message": "Metrics reset successfully"}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Failed to encode reset metrics response: %v", err)
	}
}
