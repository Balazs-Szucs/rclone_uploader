package uploader

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type Metrics struct {
	mu                 sync.RWMutex
	totalUploads       int64
	failedUploads      int64
	totalBytes         int64
	totalUploadTime    time.Duration
	lastUploadTime     time.Time
	retryCount         int64
	currentUploadStart time.Time
	currentBytes       int64
	currentDuration    time.Duration
	lastBytes          int64
	lastDuration       time.Duration
}

var globalMetrics = &Metrics{}

func (m *Metrics) RecordUploadStart() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentUploadStart = time.Now()
	m.lastUploadTime = time.Now()
	m.currentBytes = 0
	m.currentDuration = 0
	m.lastBytes = 0
	m.lastDuration = 0
}

func (m *Metrics) UpdateCurrentStats(bytes int64, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Store last values for rate calculation
	m.lastBytes = m.currentBytes
	m.lastDuration = m.currentDuration

	m.currentBytes = bytes
	m.currentDuration = duration
}

func (m *Metrics) RecordUploadComplete(bytes int64, success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.totalUploads++
	if !success {
		m.failedUploads++
	}

	// Only add bytes if we actually transferred something
	if bytes > 0 {
		m.totalBytes += bytes
	}

	// Calculate and add the duration
	uploadDuration := time.Since(m.currentUploadStart)
	m.totalUploadTime += uploadDuration
}

func (m *Metrics) RecordRetry() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retryCount++
}

// Reset clears all metrics and resets them to zero
func (m *Metrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.totalUploads = 0
	m.failedUploads = 0
	m.totalBytes = 0
	m.totalUploadTime = 0
	m.lastUploadTime = time.Time{}
	m.retryCount = 0
	m.currentUploadStart = time.Time{}
	m.currentBytes = 0
	m.currentDuration = 0
	m.lastBytes = 0
	m.lastDuration = 0
}

func (m *Metrics) GetStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Calculate success rate
	successfulUploads := m.totalUploads - m.failedUploads
	var successRate float64
	if m.totalUploads > 0 {
		successRate = float64(successfulUploads) / float64(m.totalUploads) * 100
	}

	// Calculate average upload time
	var avgUploadTime time.Duration
	if m.totalUploads > 0 {
		avgUploadTime = m.totalUploadTime / time.Duration(m.totalUploads)
	}

	// Calculate transfer rates
	var avgTransferRate float64
	if m.totalUploadTime.Seconds() > 0 && m.totalBytes > 0 {
		avgTransferRate = float64(m.totalBytes) / m.totalUploadTime.Seconds()
	}

	// Calculate current transfer rate
	var currentTransferRate float64
	if m.currentDuration.Seconds() > 0 && m.currentBytes > 0 {
		currentTransferRate = float64(m.currentBytes) / m.currentDuration.Seconds()
	}

	// Format total bytes
	totalBytesStr := "0 B"
	if m.totalBytes > 0 {
		totalBytesStr = formatBytes(m.totalBytes)
	}

	stats := map[string]interface{}{
		"total_uploads":         m.totalUploads,
		"failed_uploads":        m.failedUploads,
		"successful_uploads":    successfulUploads,
		"total_bytes":           totalBytesStr,
		"current_bytes":         formatBytes(m.currentBytes),
		"avg_upload_time":       avgUploadTime.String(),
		"retry_count":           m.retryCount,
		"success_rate":          successRate,
		"avg_transfer_rate":     formatTransferRate(avgTransferRate),
		"current_transfer_rate": formatTransferRate(currentTransferRate),
	}

	return stats
}

func GetMetrics() map[string]interface{} {
	return globalMetrics.GetStats()
}

// ResetMetrics resets all metrics to zero
func ResetMetrics() {
	globalMetrics.Reset()
}

func formatBytes(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}

	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)

	var (
		value float64
		unit  string
	)

	switch {
	case bytes >= TB:
		value = float64(bytes) / float64(TB)
		unit = "TB"
	case bytes >= GB:
		value = float64(bytes) / float64(GB)
		unit = "GB"
	case bytes >= MB:
		value = float64(bytes) / float64(MB)
		unit = "MB"
	case bytes >= KB:
		value = float64(bytes) / float64(KB)
		unit = "KB"
	default:
		return fmt.Sprintf("%d B", bytes)
	}

	// Format with 2 decimal places, but remove trailing zeros
	str := fmt.Sprintf("%.2f", value)
	str = strings.TrimRight(strings.TrimRight(str, "0"), ".")
	return fmt.Sprintf("%s %s", str, unit)
}

func formatTransferRate(bytesPerSecond float64) string {
	if bytesPerSecond == 0 {
		return "0 B/s"
	}

	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case bytesPerSecond >= GB:
		return fmt.Sprintf("%.2f GB/s", bytesPerSecond/GB)
	case bytesPerSecond >= MB:
		return fmt.Sprintf("%.2f MB/s", bytesPerSecond/MB)
	case bytesPerSecond >= KB:
		return fmt.Sprintf("%.2f KB/s", bytesPerSecond/KB)
	default:
		return fmt.Sprintf("%.2f B/s", bytesPerSecond)
	}
}
