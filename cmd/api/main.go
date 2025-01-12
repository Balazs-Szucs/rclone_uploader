package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"rclone_upload/internal/uploader"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"rclone_upload/internal/server"
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

	log.Println("Server exiting")

	done <- true
}

func main() {
	paths := make(chan string)

	go uploader.WatchDirectory(paths)

	server := server.NewServer()

	done := make(chan bool, 1)

	go gracefulShutdown(server, done)

	go func() {
		for path := range paths {
			socket, _, err := websocket.Dial(context.Background(), "ws://localhost:8080/upload", nil)
			if err != nil {
				log.Printf("Failed to connect to websocket: %v", err)
				continue
			}
			defer func(socket *websocket.Conn, code websocket.StatusCode, reason string) {
				err := socket.Close(code, reason)
				if err != nil {
					log.Printf("Failed to close websocket: %v", err)
				}
			}(socket, websocket.StatusGoingAway, "Server closing websocket")
			if err := uploader.UploadDirectory(path, socket); err != nil {
				log.Printf("Error uploading directory: %v", err)
			}
		}
	}()

	err := server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		panic(fmt.Sprintf("http server error: %s", err))
	}

	<-done
	log.Println("Graceful shutdown complete.")
}
