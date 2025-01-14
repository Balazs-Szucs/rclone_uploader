package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"github.com/joho/godotenv"
	"html/template"
	"log"
	"net/http"
	"os"
	"rclone_upload/internal/uploader"
	"time"
)

func (s *Server) RegisterRoutes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", s.healthHandler)
	mux.HandleFunc("/websocket", s.websocketHandler)
	mux.HandleFunc("/upload", s.uploadHTTPHandler)
	mux.HandleFunc("/files", s.filesHandler)
	mux.HandleFunc("/", s.indexHandler)
	mux.HandleFunc("/reset", s.resetHandler)
	mux.HandleFunc("/env", s.getEnvHandler)
	mux.HandleFunc("/update-env", s.updateEnvHandler)

	return s.corsMiddleware(mux)
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-CSRF-Token")
		w.Header().Set("Access-Control-Allow-Credentials", "false")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	resp, err := json.Marshal(s.db.Health())
	if err != nil {
		http.Error(w, "Failed to marshal health check response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(resp); err != nil {
		log.Printf("Failed to write response: %v", err)
	}
}

func (s *Server) websocketHandler(w http.ResponseWriter, r *http.Request) {
	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		http.Error(w, "Failed to open websocket", http.StatusInternalServerError)
		return
	}
	defer func(socket *websocket.Conn) {
		_, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := socket.Close(websocket.StatusGoingAway, "Server closing websocket")
		if err != nil {
			log.Printf("Failed to close websocket: %v", err)
		}
	}(socket)

	ctx := r.Context()
	socketCtx := socket.CloseRead(ctx)

	location, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		log.Printf("Failed to load location: %v", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case message := <-uploader.RcloneOutputChan:
			currentTime := time.Now().In(location).Format("2006-01-02 15:04:05")
			messageWithTime := fmt.Sprintf("%s - %s", currentTime, message)
			if err := socket.Write(socketCtx, websocket.MessageText, []byte(messageWithTime)); err != nil {
				return
			}
		case <-uploader.ResetChan:
			if err := socket.Write(socketCtx, websocket.MessageText, []byte("reset")); err != nil {
				return
			}
		case file := <-uploader.NewFileChan:
			if err := socket.Write(socketCtx, websocket.MessageText, []byte("new_file:"+file)); err != nil {
				return
			}
		}
	}
}

func (s *Server) uploadHTTPHandler(w http.ResponseWriter, r *http.Request) {
	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		http.Error(w, "Failed to open websocket", http.StatusInternalServerError)
		return
	}
	defer func(socket *websocket.Conn, code websocket.StatusCode, reason string) {
		err := socket.Close(code, reason)
		if err != nil {
			log.Printf("Failed to close websocket: %v", err)
		}
	}(socket, websocket.StatusGoingAway, "Server closing websocket")

	paths := make(chan string)
	go s.uploadHandler(paths, socket)
}

func (s *Server) uploadHandler(paths <-chan string, socket *websocket.Conn) {
	for path := range paths {
		err := uploader.UploadDirectory(path, socket)
		if err != nil {
			log.Printf("Error uploading directory: %v", err)
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
	files, err := s.db.GetUploadedFiles()
	if err != nil {
		http.Error(w, "Failed to fetch files", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(files); err != nil {
		http.Error(w, "Failed to encode files", http.StatusInternalServerError)
	}
}

func (s *Server) resetHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.db.Reset(); err != nil {
		http.Error(w, "Failed to reset database", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	uploader.ResetChan <- struct{}{}
}

func (s *Server) getEnvHandler(w http.ResponseWriter, r *http.Request) {
	envVars := map[string]string{
		"QBIT_DOWNLOADS_BASE_DIR": os.Getenv("QBIT_DOWNLOADS_BASE_DIR"),
		"RCLONE_REMOTE_NAME":      os.Getenv("RCLONE_REMOTE_NAME"),
		"WATCH_SUBDIRS":           os.Getenv("WATCH_SUBDIRS"),
		"COPY_PARAMS":             os.Getenv("COPY_PARAMS"),
	}
	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(envVars)
	if err != nil {
		return
	}
}

func (s *Server) updateEnvHandler(w http.ResponseWriter, r *http.Request) {
	var newEnvVars map[string]string
	if err := json.NewDecoder(r.Body).Decode(&newEnvVars); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	existingEnvVars, err := godotenv.Read(".env")
	if err != nil {
		http.Error(w, "Failed to read .env file", http.StatusInternalServerError)
		return
	}

	for key, value := range newEnvVars {
		existingEnvVars[key] = value
	}

	file, err := os.OpenFile(".env", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		http.Error(w, "Failed to open .env file", http.StatusInternalServerError)
		return
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			http.Error(w, "Failed to close .env file", http.StatusInternalServerError)
		}
	}(file)

	for key, value := range existingEnvVars {
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, value); err != nil {
			http.Error(w, "Failed to write to .env file", http.StatusInternalServerError)
			return
		}
	}

	if err := godotenv.Load(); err != nil {
		http.Error(w, "Failed to reload .env file", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
