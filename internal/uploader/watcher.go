package uploader

import (
	"database/sql"
	"fmt"
	"github.com/coder/websocket"
	"github.com/fsnotify/fsnotify"
	"github.com/joho/godotenv"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"rclone_upload/internal/database"
	"strings"
	"sync"
)

var uploadMutex sync.Mutex
var NewFileChan = make(chan string)
var ResetChan = make(chan struct{})

func WatchDirectory(paths chan<- string) {
	loadEnv()

	basePath := getEnv("QBIT_DOWNLOADS_BASE_DIR")
	subdirs := getEnv("WATCH_SUBDIRS")

	watcher := createWatcher()
	defer func(watcher *fsnotify.Watcher) {
		err := watcher.Close()
		if err != nil {
			log.Fatal(err)
		}
	}(watcher)

	addSubdirsToWatcher(watcher, basePath, subdirs)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create == fsnotify.Create {
				log.Printf("Created file or directory: %s", event.Name)
				paths <- event.Name
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Println("error:", err)
		}
	}
}

func loadEnv() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}
}

func getEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalf("%s not set in .env file", key)
	}
	return value
}

func createWatcher() *fsnotify.Watcher {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	return watcher
}

func addSubdirsToWatcher(watcher *fsnotify.Watcher, basePath, subdirs string) {
	subdirList := strings.Split(subdirs, ",")
	for _, subdir := range subdirList {
		path := filepath.Join(basePath, subdir)
		if err := watcher.Add(path); err != nil {
			log.Fatal(err)
		}
		log.Printf("Watching directory: %s", path)
	}
}

func UploadDirectory(path string, socket *websocket.Conn) error {
	uploadMutex.Lock()
	defer uploadMutex.Unlock()

	loadEnv()

	qbitDownloadsBaseDir := getEnv("QBIT_DOWNLOADS_BASE_DIR")
	rcloneRemoteName := getEnv("RCLONE_REMOTE_NAME")
	copyParams := getEnv("COPY_PARAMS")

	relPath, err := filepath.Rel(qbitDownloadsBaseDir, path)
	if err != nil {
		return fmt.Errorf("getting relative path: %w", err)
	}

	destPath := filepath.Join(rcloneRemoteName+":", relPath)
	args := append([]string{"copy"}, splitParams(copyParams)...)
	args = append(args, path, destPath)

	cmd := exec.Command("rclone", args...)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	log.Printf("Executing command: rclone %s", strings.Join(args, " "))

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting rclone command: %w", err)
	}

	go processRcloneOutput(stdoutPipe)

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("waiting for rclone command: %w", err)
	}

	fileName := filepath.Base(path)
	if err := saveFileToDB(fileName, path); err != nil {
		return fmt.Errorf("saving file to database: %w", err)
	}

	NewFileChan <- fileName

	return nil
}
func saveFileToDB(name, path string) error {
	db := database.New()
	query := `INSERT INTO files (name, path) VALUES (?, ?)`
	if svc, ok := db.(interface {
		Exec(query string, args ...interface{}) (sql.Result, error)
	}); ok {
		_, err := svc.Exec(query, name, path)
		return err
	}
	return fmt.Errorf("failed to assert type to interface with Exec method")
}
func processRcloneOutput(stdoutPipe io.ReadCloser) {
	buf := make([]byte, 1024)
	for {
		n, err := stdoutPipe.Read(buf)
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("Error reading from rclone stdout: %v", err)
			break
		}
		message := string(buf[:n])
		RcloneOutputChan <- message
	}
}

func splitParams(params string) []string {
	return strings.Fields(params)
}
