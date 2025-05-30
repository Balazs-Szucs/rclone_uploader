package uploader

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"rclone_uploader/internal/database"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gorilla/websocket"
)

// UploadJob represents a single upload task
type UploadJob struct {
	Path    string
	Socket  *websocket.Conn
	ID      string
	AddedAt time.Time
}

// UploadQueue manages the queue of upload jobs
type UploadQueue struct {
	queue           []UploadJob
	mutex           sync.RWMutex
	processingJob   *UploadJob
	processingMutex sync.RWMutex
}

// UploadQueueStatus represents the current state of the upload queue
type UploadQueueStatus struct {
	QueueLength   int         `json:"queueLength"`
	ProcessingJob *UploadJob  `json:"processingJob"`
	QueuedJobs    []UploadJob `json:"queuedJobs"`
}

var uploadMutex sync.Mutex
var uploadQueue = &UploadQueue{queue: make([]UploadJob, 0)}
var NewFileChan = make(chan string)
var ResetChan = make(chan struct{})
var RcloneOutputChan = make(chan string, 1000)
var UploadQueueChan = make(chan UploadJob, 1000)

// AddToQueue adds a new upload job to the queue
func (uq *UploadQueue) AddToQueue(job UploadJob) {
	uq.mutex.Lock()
	defer uq.mutex.Unlock()

	// Check if path is already in queue
	for _, existingJob := range uq.queue {
		if existingJob.Path == job.Path {
			log.Printf("Path %s already in upload queue, skipping", job.Path)
			return
		}
	}

	// Check if currently processing the same path
	uq.processingMutex.RLock()
	if uq.processingJob != nil && uq.processingJob.Path == job.Path {
		uq.processingMutex.RUnlock()
		log.Printf("Path %s is currently being processed, skipping", job.Path)
		return
	}
	uq.processingMutex.RUnlock()

	uq.queue = append(uq.queue, job)
	log.Printf("Added upload job to queue: %s (Queue length: %d)", job.Path, len(uq.queue))
}

// GetNext removes and returns the next job from the queue
func (uq *UploadQueue) GetNext() (UploadJob, bool) {
	uq.mutex.Lock()
	defer uq.mutex.Unlock()

	if len(uq.queue) == 0 {
		return UploadJob{}, false
	}

	job := uq.queue[0]
	uq.queue = uq.queue[1:]
	return job, true
}

// SetProcessing sets the currently processing job
func (uq *UploadQueue) SetProcessing(job *UploadJob) {
	uq.processingMutex.Lock()
	defer uq.processingMutex.Unlock()
	uq.processingJob = job
}

// GetStatus returns the current queue status
func (uq *UploadQueue) GetStatus() UploadQueueStatus {
	uq.mutex.RLock()
	uq.processingMutex.RLock()
	defer uq.mutex.RUnlock()
	defer uq.processingMutex.RUnlock()

	return UploadQueueStatus{
		QueueLength:   len(uq.queue),
		ProcessingJob: uq.processingJob,
		QueuedJobs:    append([]UploadJob{}, uq.queue...), // Copy to avoid race conditions
	}
}

// StartUploadWorker starts the worker that processes upload jobs from the queue
func StartUploadWorker() {
	go func() {
		log.Println("Upload worker started")
		for {
			if job, ok := uploadQueue.GetNext(); ok {
				log.Printf("Processing upload job: %s", job.Path)
				uploadQueue.SetProcessing(&job)

				// Process the upload
				err := uploadWithLock(job.Path, job.Socket)
				if err != nil {
					log.Printf("Upload failed for %s: %v", job.Path, err)
				} else {
					log.Printf("Upload completed successfully for %s", job.Path)
				}

				// Clear processing job
				uploadQueue.SetProcessing(nil)
			} else {
				// No jobs in queue, wait a bit before checking again
				time.Sleep(1 * time.Second)
			}
		}
	}()
}

// GetUploadQueueStatus returns the current upload queue status (exported function)
func GetUploadQueueStatus() UploadQueueStatus {
	return uploadQueue.GetStatus()
}

// AddUploadJob adds a new upload job to the queue (exported function)
func AddUploadJob(path string, socket *websocket.Conn) {
	job := UploadJob{
		Path:    path,
		Socket:  socket,
		ID:      fmt.Sprintf("%d-%s", time.Now().UnixNano(), filepath.Base(path)),
		AddedAt: time.Now(),
	}
	uploadQueue.AddToQueue(job)
}

// WatchDirectory monitors specified subdirectories for new files and sends their paths to the provided channel.
// It accepts a context for graceful shutdown and a channel to send file paths.
func WatchDirectory(paths chan<- string) {
	// Create a context that can be canceled when the application is shutting down
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling for graceful shutdown
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-signalChan
		log.Println("Received shutdown signal, stopping directory watcher...")
		cancel()
	}()

	// Environment variables are loaded in main.go

	basePath, err := getEnv("QBIT_DOWNLOADS_BASE_DIR")
	if err != nil {
		log.Printf("Error getting QBIT_DOWNLOADS_BASE_DIR: %v", err)
		return
	}

	subdirs, err := getEnv("WATCH_SUBDIRS")
	if err != nil {
		log.Printf("Error getting WATCH_SUBDIRS: %v", err)
		return
	}

	watcher, err := createWatcher()
	if err != nil {
		log.Printf("Error creating watcher: %v", err)
		return
	}

	defer func(watcher *fsnotify.Watcher) {
		err := watcher.Close()
		if err != nil {
			log.Printf("Error closing watcher: %v", err)
		}
	}(watcher)

	if err := addSubdirsToWatcher(watcher, basePath, subdirs); err != nil {
		log.Printf("Error adding subdirectories to watcher: %v", err)
		return
	}

	log.Printf("Directory watcher started successfully. Monitoring subdirectories: %s", subdirs)

	// Main event loop with context cancellation
	for {
		select {
		case <-ctx.Done():
			log.Println("Directory watcher shutting down...")
			return
		case event, ok := <-watcher.Events:
			if !ok {
				log.Println("Watcher event channel closed")
				return
			}
			if event.Op&fsnotify.Create == fsnotify.Create {
				log.Printf("Created file or directory: %s", event.Name)

				// Check if the path exists before sending it
				if _, err := os.Stat(event.Name); os.IsNotExist(err) {
					log.Printf("Warning: File %s no longer exists, skipping", event.Name)
					continue
				}

				// Send path with timeout to prevent blocking indefinitely
				select {
				case paths <- event.Name:
					// Successfully sent
				case <-time.After(5 * time.Second):
					log.Printf("Warning: Timed out sending path %s to channel", event.Name)
				case <-ctx.Done():
					return
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				log.Println("Watcher error channel closed")
				return
			}
			log.Printf("Watcher error: %v", err)
		}
	}
}

// getEnv gets an environment variable or returns an error if it's not set
func getEnv(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s not set in .env file", key)
	}
	return value, nil
}

func createWatcher() (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("error creating watcher: %w", err)
	}
	return watcher, nil
}

func addSubdirsToWatcher(watcher *fsnotify.Watcher, basePath, subdirs string) error {
	subdirList := strings.Split(subdirs, ",")
	var watchErrors []error

	for _, subdir := range subdirList {
		path := filepath.Join(basePath, subdir)

		// Check if directory exists before adding to watcher
		if _, err := os.Stat(path); os.IsNotExist(err) {
			log.Printf("Warning: Directory %s does not exist, skipping", path)
			continue
		}

		if err := watcher.Add(path); err != nil {
			watchErrors = append(watchErrors, fmt.Errorf("error adding directory %s to watcher: %w", path, err))
			log.Printf("Error adding directory %s to watcher: %v", path, err)
			continue
		}
		log.Printf("Watching directory: %s", path)
	}

	// If all directories failed to watch, return an error
	if len(watchErrors) > 0 && len(watchErrors) == len(subdirList) {
		return fmt.Errorf("failed to watch any directories: %v", watchErrors)
	}

	return nil
}

func UploadDirectory(path string, socket *websocket.Conn) error {
	// Get max retries from environment or use default
	maxRetriesStr := os.Getenv("MAX_RETRIES")
	maxRetries := 3
	if maxRetriesStr != "" {
		if val, err := strconv.Atoi(maxRetriesStr); err == nil && val > 0 {
			maxRetries = val
		}
	}

	// Get retry delay from environment or use default
	retryDelayStr := os.Getenv("RETRY_DELAY")
	retryDelay := 5 * time.Second
	if retryDelayStr != "" {
		if val, err := strconv.Atoi(retryDelayStr); err == nil && val > 0 {
			retryDelay = time.Duration(val) * time.Second
		}
	}

	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			log.Printf("Retry attempt %d/%d for path: %s", attempt, maxRetries, path)
			time.Sleep(time.Duration(attempt) * retryDelay)
		}

		err := uploadWithLock(path, socket)
		if err == nil {
			return nil
		}

		lastErr = err
		log.Printf("Upload attempt %d failed: %v", attempt, err)

		if !isRetryableError(err) {
			return fmt.Errorf("non-retryable error: %w", err)
		}
	}

	return fmt.Errorf("failed after %d attempts, last error: %w", maxRetries, lastErr)
}

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// Add conditions for retryable errors
	return strings.Contains(err.Error(), "connection reset") ||
		strings.Contains(err.Error(), "timeout") ||
		strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "no such host") ||
		strings.Contains(err.Error(), "temporary")
}

func uploadWithLock(path string, socket *websocket.Conn) error {
	uploadMutex.Lock()
	defer uploadMutex.Unlock()

	// Check if the path exists before attempting to upload
	if _, err := os.Stat(path); os.IsNotExist(err) {
		log.Printf("Error: Path %s does not exist, skipping upload", path)
		return fmt.Errorf("path does not exist: %s", path)
	}

	qbitDownloadsBaseDir, err := getEnv("QBIT_DOWNLOADS_BASE_DIR")
	if err != nil {
		return fmt.Errorf("getting QBIT_DOWNLOADS_BASE_DIR: %w", err)
	}

	rcloneRemoteName, err := getEnv("RCLONE_REMOTE_NAME")
	if err != nil {
		return fmt.Errorf("getting RCLONE_REMOTE_NAME: %w", err)
	}

	copyParams, err := getEnv("COPY_PARAMS")
	if err != nil {
		return fmt.Errorf("getting COPY_PARAMS: %w", err)
	}

	relPath, err := filepath.Rel(qbitDownloadsBaseDir, path)
	if err != nil {
		return fmt.Errorf("getting relative path: %w", err)
	}

	// Ensure the remote name is valid
	if rcloneRemoteName == "" {
		return fmt.Errorf("RCLONE_REMOTE_NAME is empty")
	}

	destPath := filepath.Join(rcloneRemoteName+"", relPath)
	args := append([]string{"copy"}, splitParams(copyParams)...)
	args = append(args, path, destPath)

	// Get upload timeout from environment or use default
	uploadTimeoutStr := os.Getenv("UPLOAD_TIMEOUT")
	uploadTimeout := 30 * 60 * time.Second // 30 minute default
	if uploadTimeoutStr != "" {
		if val, err := strconv.Atoi(uploadTimeoutStr); err == nil && val > 0 {
			uploadTimeout = time.Duration(val) * time.Second
		}
	}

	// Create a context with timeout to prevent hanging processes
	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "rclone", args...)

	// Capture both stdout and stderr
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("creating stderr pipe: %w", err)
	}

	log.Printf("Executing command: rclone %s", strings.Join(args, " "))

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting rclone command: %w", err)
	}

	// Use a WaitGroup to ensure output processing completes before function returns
	var wg sync.WaitGroup
	wg.Add(2)

	// Process stdout
	go func() {
		defer wg.Done()
		processRcloneOutput(stdoutPipe, socket)
	}()

	// Process stderr
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			errMsg := scanner.Text()
			log.Printf("rclone error: %s", errMsg)

			// Send error message to websocket if available
			if socket != nil {
				if err := socket.WriteMessage(websocket.TextMessage, []byte("ERROR: "+errMsg)); err != nil {
					log.Printf("Failed to write error message to websocket: %v", err)
				}
			}
		}
	}()

	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("rclone command timed out after 30 minutes")
		}
		return fmt.Errorf("waiting for rclone command: %w", err)
	}

	// Wait for output processing to complete
	wg.Wait()

	fileName := filepath.Base(path)
	if err := saveFileToDB(fileName, path); err != nil {
		return fmt.Errorf("saving file to database: %w", err)
	}

	// Send notification about new file
	select {
	case NewFileChan <- fileName:
		log.Printf("Successfully uploaded and notified about file: %s", fileName)
	default:
		log.Printf("Warning: Could not send new file notification, channel might be full")
	}

	return nil
}

func saveFileToDB(name, path string) error {
	log.Printf("Saving file to database: name=%s, path=%s", name, path)
	db := database.New()
	defer db.Close()

	// Get DB timeout from environment or use default
	dbTimeoutStr := os.Getenv("DB_TIMEOUT")
	dbTimeout := 5 * time.Second // 5 second default
	if dbTimeoutStr != "" {
		if val, err := strconv.Atoi(dbTimeoutStr); err == nil && val > 0 {
			dbTimeout = time.Duration(val) * time.Second
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	query := `INSERT INTO files (name, path) VALUES (?, ?)`
	result, err := db.Exec(ctx, query, name, path)
	if err != nil {
		log.Printf("Error saving file to database: %v", err)
		return fmt.Errorf("failed to save file to database: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		log.Printf("Error getting last insert ID: %v", err)
	} else {
		log.Printf("Successfully saved file to database with ID: %d", id)
	}

	return nil
}

func processRcloneOutput(stdoutPipe io.ReadCloser, socket *websocket.Conn) {
	scanner := bufio.NewScanner(stdoutPipe)
	const maxCapacity = 512 * 1024 // 512KB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	var currentBlock []string
	var lastTransferLine string

	// Start tracking upload metrics
	log.Printf("Recording upload start for metrics")
	globalMetrics.RecordUploadStart()
	var transferredBytes int64
	var hasError bool
	startTime := time.Now()
	var lastUpdate time.Time

	// Track file size from initial stats
	var totalFileSize int64 = -1

	for scanner.Scan() {
		message := scanner.Text()
		currentBlock = append(currentBlock, message)

		// Look for file size in stats line
		if strings.Contains(message, "Transferred:") {
			parts := strings.Split(message, ",")
			if len(parts) > 0 {
				transferPart := strings.TrimPrefix(parts[0], "Transferred:")
				transferPart = strings.TrimSpace(transferPart)

				// Split by "/" to get both transferred and total
				transferredParts := strings.Split(transferPart, "/")
				if len(transferredParts) == 2 {
					// Parse transferred amount
					transferredStr := strings.TrimSpace(transferredParts[0])
					if !strings.Contains(transferredStr, "*") { // Skip parsing if contains asterisk
						if bytes, err := parseSize(transferredStr); err == nil {
							transferredBytes = bytes
							log.Printf("Current transfer progress: %s (%d bytes)", transferredStr, bytes)

							// Update metrics every second
							if time.Since(lastUpdate) >= time.Second {
								duration := time.Since(startTime)
								globalMetrics.UpdateCurrentStats(transferredBytes, duration)
								lastUpdate = time.Now()
							}
						}
					}

					// Parse total size if we haven't yet
					if totalFileSize == -1 {
						totalStr := strings.TrimSpace(transferredParts[1])
						if bytes, err := parseSize(totalStr); err == nil {
							totalFileSize = bytes
							log.Printf("Total file size: %d bytes", totalFileSize)
						}
					}
				}
				lastTransferLine = message
			}
		}

		// Check for error messages
		if strings.Contains(strings.ToLower(message), "error") ||
			strings.Contains(strings.ToLower(message), "failed") {
			hasError = true
			log.Printf("Upload error detected: %s", message)
		}

		// Look for completion messages
		if strings.Contains(message, "100%") || strings.Contains(message, "Transferred:") && strings.Contains(message, "100%") {
			if totalFileSize > 0 {
				transferredBytes = totalFileSize // Use total size as final transferred amount
			}
		}

		// If this line starts with "Transferring:", send the current block
		if strings.HasPrefix(strings.TrimSpace(message), "Transferring:") {
			if len(currentBlock) > 0 {
				completeStatus := strings.Join(currentBlock, "\n")

				select {
				case RcloneOutputChan <- completeStatus:
				default:
					log.Printf("Warning: RcloneOutputChan is full: %s", completeStatus)
				}

				if socket != nil {
					if err := socket.WriteMessage(websocket.TextMessage, []byte(completeStatus)); err != nil {
						log.Printf("Failed to write message to websocket: %v", err)
					}
				}

				if lastTransferLine != "" {
					currentBlock = []string{lastTransferLine}
				} else {
					currentBlock = nil
				}
			}
		}
	}

	// Record final metrics
	duration := time.Since(startTime)
	finalBytes := transferredBytes
	if totalFileSize > 0 {
		finalBytes = totalFileSize // Use total size if available
	}

	if finalBytes > 0 {
		log.Printf("Upload completed - Final stats: %d bytes in %v", finalBytes, duration)
		// Ensure we update the final stats
		globalMetrics.UpdateCurrentStats(finalBytes, duration)
	} else {
		log.Printf("Warning: Upload completed with 0 bytes transferred (total size: %d)", totalFileSize)
	}

	// Record completion with final byte count
	log.Printf("Recording upload complete for metrics: %d bytes, success: %t", finalBytes, !hasError)
	globalMetrics.RecordUploadComplete(finalBytes, !hasError)

	if err := scanner.Err(); err != nil {
		log.Printf("Error reading from rclone stdout: %v", err)
		hasError = true
	}
}

func parseSize(sizeStr string) (int64, error) {
	parts := strings.Fields(sizeStr)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid size format")
	}

	value, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, err
	}

	multiplier := int64(1)
	unit := strings.ToUpper(parts[1])
	switch unit {
	case "B":
		multiplier = 1
	case "KB", "KIB":
		multiplier = 1024
	case "MB", "MIB":
		multiplier = 1024 * 1024
	case "GB", "GIB":
		multiplier = 1024 * 1024 * 1024
	case "TB", "TIB":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown unit: %s", unit)
	}

	return int64(value * float64(multiplier)), nil
}

func splitParams(params string) []string {
	return strings.Fields(params)
}
