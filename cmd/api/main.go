package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rclone_uploader/internal/logging"
	"rclone_uploader/internal/server"
	"rclone_uploader/internal/uploader"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

func gracefulShutdown(apiServer *http.Server, done chan bool) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()

	log.Println("shutting down gracefully, press Ctrl+C again to force")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := apiServer.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown with error: %v", err)
	}

	// Shutdown logger
	if err := logging.Shutdown(); err != nil {
		log.Printf("Error shutting down logger: %v", err)
	}

	log.Println("Server exiting")

	done <- true
}

func main() {
	// Initialize logger
	logConfig := logging.DefaultConfig()
	if err := logging.InitLogger(logConfig); err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}

	// Get logger instance
	logger := logging.Logger()

	// Load environment variables
	if err := loadEnvVars(); err != nil {
		logger.Fatalw("Failed to load environment variables", "error", err)
	}

	// Create a buffered channel for paths to prevent blocking
	paths := make(chan string, 1000)

	// Start the directory watcher in a goroutine
	go uploader.WatchDirectory(paths)

	// Create a new server
	server := server.NewServer()

	// Channel for graceful shutdown completion
	done := make(chan bool, 1)

	// Start graceful shutdown handler
	go gracefulShutdown(server, done)

	// Get port from environment or use default
	port := os.Getenv("PORT")
	if port == "" {
		port = "8050"
		logger.Infow("PORT not set, using default", "port", port)
	}

	// Get websocket URL from environment or construct default
	wsURL := os.Getenv("WEBSOCKET_URL")
	if wsURL == "" {
		wsURL = fmt.Sprintf("ws://localhost:%s/websocket", port)
		logger.Infow("WEBSOCKET_URL not set, using default", "url", wsURL)
	}

	// Start the upload worker to process upload queue
	uploader.StartUploadWorker()

	// Start the path processor in a goroutine
	go processPathsWithQueue(paths, wsURL)

	// Start the HTTP server
	logger.Infow("Starting server", "port", port)
	err := server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalw("HTTP server error", "error", err)
	}

	// Wait for graceful shutdown to complete
	<-done
	logger.Info("Graceful shutdown complete.")
}

// loadEnvVars loads environment variables from .env file
func loadEnvVars() error {
	logger := logging.Logger()

	appEnv := os.Getenv("APP_ENV")
	if appEnv == "production" {
		logger.Info("Running in production mode, skipping .env file loading.")
		return nil
	}

	// Check if .env file exists
	if _, err := os.Stat(".env"); os.IsNotExist(err) {
		logger.Warn("Warning: .env file not found, using environment variables")
		return nil
	}

	// Load .env file
	if err := godotenv.Load(); err != nil {
		// Log the error but don't return it to prevent application from exiting
		// This is especially useful in Docker environments where env vars are passed directly
		logger.Warn("Error loading .env file, continuing with environment variables", "error", err)
		return nil
	}

	logger.Info("Environment variables loaded from .env file")
	return nil
}

// processPathsWithQueue processes paths from the channel and adds them to upload queue
func processPathsWithQueue(paths chan string, wsURL string) {
	logger := logging.Logger()
	var socket *websocket.Conn
	var err error

	// Create a dialer with timeout
	dialer := &websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	// Map to track retry attempts for websocket connection
	retryCount := 0
	maxRetries := 3

	for path := range paths {
		// Check if the path exists before attempting to process it
		if _, err := os.Stat(path); os.IsNotExist(err) {
			logger.Warnw("Path no longer exists, skipping", "path", path)
			continue
		}

		// Try to connect if we don't have a connection
		if socket == nil {
			logger.Infow("Connecting to websocket", "url", wsURL)
			socket, _, err = dialer.Dial(wsURL, nil)
			if err != nil {
				logger.Errorw("Failed to connect to websocket", "error", err)
				retryCount++

				if retryCount >= maxRetries {
					logger.Warnw("Max websocket connection retries exceeded, adding upload job without websocket", "path", path)
					// Add to queue without websocket
					uploader.AddUploadJob(path, nil)
					retryCount = 0 // Reset for next connection attempt
					continue
				}

				// Wait and try again with exponential backoff
				backoff := time.Duration(retryCount) * 5 * time.Second
				time.Sleep(backoff)
				// Put the path back for retry
				go func(p string) {
					paths <- p
				}(path)
				continue
			}
			retryCount = 0 // Reset retry count on successful connection
		}

		// Add the upload job to the queue
		logger.Infow("Adding upload job to queue", "path", path)
		uploader.AddUploadJob(path, socket)
	}

	// Close the socket when done
	if socket != nil {
		if err := socket.Close(); err != nil {
			logger.Errorw("Failed to close websocket", "error", err)
		}
	}
}
