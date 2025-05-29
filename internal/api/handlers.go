package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"rclone_uploader/internal/database"
	"rclone_uploader/internal/errors"
)

// FileInfo represents an uploaded file's information
type FileInfo struct {
	Name       string `json:"name"`
	UploadedAt string `json:"uploaded_at"`
}

// Metrics represents upload metrics
type Metrics struct {
	TotalUploads    int64   `json:"total_uploads"`
	FailedUploads   int64   `json:"failed_uploads"`
	TotalBytes      int64   `json:"total_bytes"`
	CurrentBytes    int64   `json:"current_bytes"`
	AvgTransferRate string  `json:"avg_transfer_rate"`
	CurrentRate     string  `json:"current_rate"`
	AvgUploadTime   string  `json:"avg_upload_time"`
	RetryCount      int64   `json:"retry_count"`
	SuccessRate     float64 `json:"success_rate"`
}

type uploadStat struct {
	startTime      time.Time
	endTime        time.Time
	bytes          int64
	lastBytes      int64
	lastUpdateTime time.Time
	currentRate    float64
	isCompleted    bool
	isFailed       bool
	retryCount     int
}

var (
	metricsLock    sync.RWMutex
	uploadStatsMap = make(map[string]*uploadStat)
	metrics        = &Metrics{}

	// Regular expressions for parsing rclone output
	transferredRegex = regexp.MustCompile(`Transferred:\s+(\d+\.\d+\s*[KMGT]?B)`)
	speedRegex       = regexp.MustCompile(`(\d+\.\d+\s*[KMGT]?B/s)$`)
)

// ParseSize converts a human-readable size string to bytes
func ParseSize(size string) int64 {
	size = strings.TrimSpace(size)
	if size == "" {
		return 0
	}

	multipliers := map[string]int64{
		"B":  1,
		"KB": 1024,
		"MB": 1024 * 1024,
		"GB": 1024 * 1024 * 1024,
		"TB": 1024 * 1024 * 1024 * 1024,
	}

	var value float64
	var unit string
	_, err := fmt.Sscanf(size, "%f%s", &value, &unit)
	if err != nil {
		return 0
	}

	multiplier := multipliers[strings.ToUpper(unit)]
	return int64(value * float64(multiplier))
}

// UpdateUploadProgress updates the progress of an ongoing upload
func UpdateUploadProgress(id string, output string) {
	metricsLock.Lock()
	defer metricsLock.Unlock()

	stats, exists := uploadStatsMap[id]
	if !exists {
		stats = &uploadStat{
			startTime:      time.Now(),
			lastUpdateTime: time.Now(),
		}
		uploadStatsMap[id] = stats
	}

	// Parse transferred amount
	if matches := transferredRegex.FindStringSubmatch(output); len(matches) > 1 {
		currentBytes := ParseSize(matches[1])
		timeSinceLastUpdate := time.Since(stats.lastUpdateTime).Seconds()

		if timeSinceLastUpdate > 0 {
			bytesDelta := currentBytes - stats.lastBytes
			stats.currentRate = float64(bytesDelta) / timeSinceLastUpdate
		}

		stats.bytes = currentBytes
		stats.lastBytes = currentBytes
		stats.lastUpdateTime = time.Now()
	}

	// Parse speed directly from output
	if matches := speedRegex.FindStringSubmatch(output); len(matches) > 1 {
		speedStr := matches[1]
		speedStr = strings.TrimSuffix(speedStr, "/s")
		bytesPerSecond := ParseSize(speedStr)
		if bytesPerSecond > 0 {
			stats.currentRate = float64(bytesPerSecond)
		}
	}

	updateMetrics()
}

// CompleteUpload marks an upload as completed
func CompleteUpload(id string, output string) {
	metricsLock.Lock()
	defer metricsLock.Unlock()

	stats, exists := uploadStatsMap[id]
	if !exists {
		stats = &uploadStat{
			startTime: time.Now(),
		}
		uploadStatsMap[id] = stats
	}

	// Parse final transferred amount
	if matches := transferredRegex.FindStringSubmatch(output); len(matches) > 1 {
		stats.bytes = ParseSize(matches[1])
	}

	stats.endTime = time.Now()
	stats.isCompleted = true
	updateMetrics()
}

// FailUpload marks an upload as failed
func FailUpload(id string) {
	metricsLock.Lock()
	defer metricsLock.Unlock()

	stats, exists := uploadStatsMap[id]
	if !exists {
		stats = &uploadStat{
			startTime: time.Now(),
		}
		uploadStatsMap[id] = stats
	}

	stats.endTime = time.Now()
	stats.isFailed = true
	updateMetrics()
}

// RecordRetry increments the retry count for an upload
func RecordRetry(id string) {
	metricsLock.Lock()
	defer metricsLock.Unlock()

	stats, exists := uploadStatsMap[id]
	if !exists {
		stats = &uploadStat{
			startTime: time.Now(),
		}
		uploadStatsMap[id] = stats
	}

	stats.retryCount++
	updateMetrics()
}

func updateMetrics() {
	var (
		totalUploads   int64
		failedUploads  int64
		totalBytes     int64
		currentBytes   int64
		totalTime      time.Duration
		retryCount     int64
		completedCount int64
		currentRate    float64
		totalRate      float64
	)

	// Calculate metrics
	for _, stats := range uploadStatsMap {
		if stats.isCompleted || stats.isFailed {
			totalUploads++
			if stats.isFailed {
				failedUploads++
			} else {
				completedCount++
				totalBytes += stats.bytes
				totalTime += stats.endTime.Sub(stats.startTime)
				if stats.endTime.Sub(stats.startTime).Seconds() > 0 {
					totalRate += float64(stats.bytes) / stats.endTime.Sub(stats.startTime).Seconds()
				}
			}
		} else {
			currentBytes += stats.bytes
			currentRate += stats.currentRate
		}
		retryCount += int64(stats.retryCount)
	}

	// Update metrics struct
	metrics.TotalUploads = totalUploads
	metrics.FailedUploads = failedUploads
	metrics.TotalBytes = totalBytes
	metrics.CurrentBytes = currentBytes
	metrics.RetryCount = retryCount

	// Calculate success rate
	if totalUploads > 0 {
		metrics.SuccessRate = float64(completedCount) / float64(totalUploads) * 100
	}

	// Calculate average upload time
	if completedCount > 0 {
		avgTime := totalTime / time.Duration(completedCount)
		metrics.AvgUploadTime = avgTime.String()
	}

	// Set transfer rates
	if completedCount > 0 {
		metrics.AvgTransferRate = formatTransferRate(totalRate / float64(completedCount))
	}

	if currentRate > 0 {
		metrics.CurrentRate = formatTransferRate(currentRate)
	}
}

func formatTransferRate(bytesPerSecond float64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case bytesPerSecond >= GB:
		return fmt.Sprintf("%.2f GB/s", bytesPerSecond/GB)
	case bytesPerSecond >= MB:
		return fmt.Sprintf("%.2f MB/s", bytesPerSecond/MB)
	case bytesPerSecond >= KB:
		return fmt.Sprintf("%.2f KB/s", bytesPerSecond/KB)
	default:
		return fmt.Sprintf("%.0f B/s", bytesPerSecond)
	}
}

// HandleFiles handles the /files endpoint
func HandleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errors.SendError(w, errors.NewError(errors.ErrorTypeValidation, "Method not allowed", nil))
		return
	}

	files, err := getUploadedFiles()
	if err != nil {
		errors.SendError(w, errors.NewError(errors.ErrorTypeSystem, "Failed to get uploaded files", err))
		return
	}

	sendJSON(w, files)
}

// HandleReset handles the /reset endpoint
func HandleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errors.SendError(w, errors.NewError(errors.ErrorTypeValidation, "Method not allowed", nil))
		return
	}

	if err := resetDatabase(); err != nil {
		errors.SendError(w, errors.NewError(errors.ErrorTypeSystem, "Failed to reset database", err))
		return
	}

	w.WriteHeader(http.StatusOK)
}

// HandleMetrics handles the /metrics endpoint
func HandleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errors.SendError(w, errors.NewError(errors.ErrorTypeValidation, "Method not allowed", nil))
		return
	}

	metricsLock.RLock()
	metricsSnapshot := *metrics // Create a copy of current metrics
	metricsLock.RUnlock()

	sendJSON(w, metricsSnapshot)
}

// ResetMetrics resets all metrics to zero
func ResetMetrics() {
	metricsLock.Lock()
	defer metricsLock.Unlock()

	uploadStatsMap = make(map[string]*uploadStat)
	metrics = &Metrics{}
}

// Helper function to send JSON response
func sendJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		errors.SendError(w, errors.NewError(errors.ErrorTypeSystem, "Failed to encode response", err))
		return
	}
}

// Database operations
func getUploadedFiles() ([]FileInfo, error) {
	db := database.New()
	defer db.Close()

	ctx := context.Background()
	files, err := db.GetUploadedFiles(ctx)
	if err != nil {
		return nil, err
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

	return fileInfos, nil
}

func resetDatabase() error {
	db := database.New()
	defer db.Close()

	ctx := context.Background()
	return db.Reset(ctx)
}

// getMetrics retrieves metrics from the database and memory
func getMetrics() (*Metrics, error) {
	metricsLock.RLock()
	defer metricsLock.RUnlock()

	// Return a copy of the current metrics
	return &Metrics{
		TotalUploads:    metrics.TotalUploads,
		FailedUploads:   metrics.FailedUploads,
		TotalBytes:      metrics.TotalBytes,
		CurrentBytes:    metrics.CurrentBytes,
		AvgTransferRate: metrics.AvgTransferRate,
		CurrentRate:     metrics.CurrentRate,
		AvgUploadTime:   metrics.AvgUploadTime,
		RetryCount:      metrics.RetryCount,
		SuccessRate:     metrics.SuccessRate,
	}, nil
}

// RecordUploadedFile records a file upload in the database
func RecordUploadedFile(name, path string) error {
	db := database.New()
	defer db.Close()

	ctx := context.Background()
	query := `INSERT INTO files (name, path) VALUES (?, ?)`
	_, err := db.Exec(ctx, query, name, path)
	return err
}
