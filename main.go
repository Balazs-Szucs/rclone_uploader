package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Config holds the application configuration
type Config struct {
	QbitDownloadsBaseDir string   `json:"qbit_downloads_basedir"`
	WatchDirs            []string `json:"watch_dirs"`
	CopyParams           string   `json:"copy_params"`
	RcloneRemoteName     string   `json:"rclone_remote_name"`
	RemoteStatus         []string `json:"remote_status"`
}

// AppState holds the application's runtime state
type AppState struct {
	config             Config
	uploadedFiles      map[string]bool
	uploadedFilesMutex sync.RWMutex
	stats              struct {
		data  string
		mutex sync.RWMutex
	}
}

// StatusUpdate represents the current status of file transfers
type StatusUpdate struct {
	ElapsedTime    string
	FileProgresses []string
	Transferred    string
}

// NewAppState creates and initializes a new application state
func NewAppState() *AppState {
	return &AppState{
		uploadedFiles: make(map[string]bool),
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app := NewAppState()
	if err := app.initialize(); err != nil {
		log.Fatal("Failed to initialize application:", err)
	}

	server := &http.Server{
		Addr:         ":8050",
		Handler:      app.setupRoutes(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Handle graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		}
		cancel()
	}()

	go app.watchDirectories(ctx)

	log.Println("Server starting on :8050")
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal("Server error:", err)
	}
}

func (app *AppState) initialize() error {
	if err := app.loadConfig(); err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if err := app.loadUploadedFiles(); err != nil {
		return fmt.Errorf("loading uploaded files: %w", err)
	}

	return nil
}

func (app *AppState) loadConfig() error {
	file, err := os.Open("config.json")
	if err != nil {
		return fmt.Errorf("opening config file: %w", err)
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {

		}
	}(file)

	if err := json.NewDecoder(file).Decode(&app.config); err != nil {
		return fmt.Errorf("decoding config: %w", err)
	}

	log.Printf("Configuration loaded: %+v", app.config)
	return nil
}

func (app *AppState) loadUploadedFiles() error {
	file, err := os.Open("uploaded_files.txt")
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("uploaded_files.txt does not exist, starting fresh")
			return nil
		}
		return fmt.Errorf("opening uploaded_files.txt: %w", err)
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			log.Printf("Error closing uploaded_files.txt: %v", err)
		}
	}(file)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		app.uploadedFiles[scanner.Text()] = true
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading uploaded_files.txt: %w", err)
	}

	return nil
}

func (app *AppState) saveUploadedFile(path string) error {
	file, err := os.OpenFile("uploaded_files.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening uploaded_files.txt for writing: %w", err)
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			log.Printf("Error closing uploaded_files.txt: %v", err)
		}
	}(file)

	if _, err := file.WriteString(path + "\n"); err != nil {
		return fmt.Errorf("writing to uploaded_files.txt: %w", err)
	}

	return nil
}

func (app *AppState) watchDirectories(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal("Error creating watcher:", err)
	}
	defer func(watcher *fsnotify.Watcher) {
		err := watcher.Close()
		if err != nil {
			log.Printf("Error closing watcher: %v", err)
		}
	}(watcher)

	for _, dir := range app.config.WatchDirs {
		fullPath := filepath.Join(app.config.QbitDownloadsBaseDir, dir)
		if err := watcher.Add(fullPath); err != nil {
			log.Printf("Error watching directory %s: %v", fullPath, err)
			continue
		}
		log.Printf("Watching directory: %s", fullPath)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create == fsnotify.Create {
				go app.handleNewFile(event.Name)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Println("Watcher error:", err)
		}
	}
}

func (app *AppState) handleNewFile(path string) {
	if app.isFileUploaded(path) {
		log.Printf("File %s has already been uploaded, skipping", path)
		return
	}

	if err := app.uploadFile(path); err != nil {
		log.Printf("Error uploading file %s: %v", path, err)
		return
	}

	if err := app.markFileAsUploaded(path); err != nil {
		log.Printf("Error marking file as uploaded %s: %v", path, err)
	}
}

func (app *AppState) uploadFile(path string) error {
	relPath, err := filepath.Rel(app.config.QbitDownloadsBaseDir, path)
	if err != nil {
		return fmt.Errorf("getting relative path: %w", err)
	}

	destPath := filepath.Join(app.config.RcloneRemoteName+":", relPath)
	args := append([]string{"copy"}, splitParams(app.config.CopyParams)...)
	args = append(args, path, destPath)

	cmd := exec.Command("rclone", args...)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting rclone command: %w", err)
	}

	go app.processRcloneOutput(stdoutPipe)

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("waiting for rclone command: %w", err)
	}

	app.updateStats(fmt.Sprintf("Successfully uploaded %s to %s\n", path, destPath))
	return nil
}

func (app *AppState) isFileUploaded(path string) bool {
	app.uploadedFilesMutex.RLock()
	defer app.uploadedFilesMutex.RUnlock()
	return app.uploadedFiles[path]
}

func (app *AppState) markFileAsUploaded(path string) error {
	app.uploadedFilesMutex.Lock()
	app.uploadedFiles[path] = true
	app.uploadedFilesMutex.Unlock()

	return app.saveUploadedFile(path)
}

func (app *AppState) processRcloneOutput(stdoutPipe io.ReadCloser) {
	reader := bufio.NewReader(stdoutPipe)
	var currentUpdate StatusUpdate
	var buffer strings.Builder

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				log.Printf("Error reading stdout: %v", err)
			}
			return
		}

		line = strings.TrimSpace(line)
		app.updateStatusFromLine(line, &currentUpdate, &buffer)
	}
}

func (app *AppState) updateStatusFromLine(line string, update *StatusUpdate, buffer *strings.Builder) {
	if strings.Contains(line, "Transferred:") && strings.Contains(line, "* ") {
		parts := strings.Split(line, "Transferred:")
		if len(parts) == 2 {
			filePart := strings.TrimSpace(parts[0])
			if strings.HasPrefix(filePart, "* ") {
				update.FileProgresses = append(update.FileProgresses, filePart)
			}
			update.Transferred = "Transferred:" + parts[1]
		}
		return
	}

	switch {
	case strings.HasPrefix(line, "Elapsed time:"):
		update.ElapsedTime = line
	case strings.HasPrefix(line, "* "):
		update.FileProgresses = append(update.FileProgresses, line)
	case strings.HasPrefix(line, "Transferred:"):
		update.Transferred = line
	}

	if update.Transferred != "" {
		app.formatAndUpdateStats(update, buffer)
		*update = StatusUpdate{}
	}
}

func (app *AppState) formatAndUpdateStats(update *StatusUpdate, buffer *strings.Builder) {
	buffer.Reset()

	if update.ElapsedTime != "" {
		buffer.WriteString(update.ElapsedTime)
		buffer.WriteString("\n\n")
	}

	if len(update.FileProgresses) > 0 {
		buffer.WriteString("Transferring Files:\n")
		for _, progress := range update.FileProgresses {
			buffer.WriteString(progress)
			buffer.WriteString("\n")
		}
		buffer.WriteString("\n")
	}

	buffer.WriteString("Overall Progress:\n")
	buffer.WriteString(update.Transferred)
	buffer.WriteString("\n")

	app.updateStats(buffer.String())
}

func (app *AppState) updateStats(output string) {
	app.stats.mutex.Lock()
	defer app.stats.mutex.Unlock()
	app.stats.data = output
}

func (app *AppState) setupRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/update-config", app.handleUpdateConfig)
	mux.HandleFunc("/stats", app.handleStats)
	return mux
}

func (app *AppState) handleIndex(w http.ResponseWriter, _ *http.Request) {
	tmpl, err := template.ParseFiles("index.html")
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		log.Printf("Error parsing index.html template: %v", err)
		return
	}

	if err := tmpl.Execute(w, app.config); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		log.Printf("Error executing index.html template: %v", err)
	}
}

func (app *AppState) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		log.Printf("Error parsing form: %v", err)
		return
	}

	newConfig := Config{
		QbitDownloadsBaseDir: r.FormValue("qbit_downloads_basedir"),
		WatchDirs:            strings.Split(r.FormValue("watch_dirs"), ","),
		CopyParams:           r.FormValue("copy_params"),
		RcloneRemoteName:     r.FormValue("rclone_remote_name"),
	}

	if err := app.saveConfig(newConfig); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		log.Printf("Error saving config: %v", err)
		return
	}

	app.config = newConfig
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (app *AppState) saveConfig(config Config) error {
	file, err := os.Create("config.json")
	if err != nil {
		return fmt.Errorf("creating config.json: %w", err)
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			log.Printf("Error closing config file: %v", err)
		}
	}(file)

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	return nil
}

func (app *AppState) handleStats(w http.ResponseWriter, _ *http.Request) {
	app.stats.mutex.RLock()
	defer app.stats.mutex.RUnlock()

	if _, err := fmt.Fprint(w, app.stats.data); err != nil {
		log.Printf("Error writing stats data: %v", err)
	}
}

func splitParams(s string) []string {
	var params []string
	var current strings.Builder
	inQuotes := false

	for _, r := range s {
		switch {
		case r == '"' && !inQuotes:
			inQuotes = true
		case r == ' ' && !inQuotes:
			if current.Len() > 0 {
				params = append(params, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}

	if current.Len() > 0 {
		params = append(params, current.String())
	}

	return params
}
