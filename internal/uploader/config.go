package uploader

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all configuration for the uploader
type Config struct {
	BaseDir          string
	WatchDirs        []string
	RcloneRemoteName string
	CopyParams       []string
	MaxRetries       int
	RetryDelay       time.Duration
	UploadTimeout    time.Duration
	DBTimeout        time.Duration
}

// LoadConfig loads configuration from environment variables with sensible defaults
func LoadConfig() (*Config, error) {
	// Get base directory with default
	baseDir := getEnvWithDefault("QBIT_DOWNLOADS_BASE_DIR", "/downloads")

	// Get watch directories with default
	watchDirsStr := getEnvWithDefault("WATCH_SUBDIRS", "s,m,seed")
	watchDirs := strings.Split(watchDirsStr, ",")

	// Get remote name with default
	remoteName := getEnvWithDefault("RCLONE_REMOTE_NAME", "remote")

	// Get copy parameters with default
	copyParamsStr := getEnvWithDefault("COPY_PARAMS", "--progress --ignore-existing")
	copyParams := strings.Fields(copyParamsStr)

	// Get max retries with default
	maxRetries := getEnvIntWithDefault("MAX_RETRIES", 3)

	// Get retry delay with default
	retryDelay := time.Duration(getEnvIntWithDefault("RETRY_DELAY", 5)) * time.Second

	// Get upload timeout with default
	uploadTimeout := time.Duration(getEnvIntWithDefault("UPLOAD_TIMEOUT", 30*60)) * time.Second

	// Get DB timeout with default
	dbTimeout := time.Duration(getEnvIntWithDefault("DB_TIMEOUT", 5)) * time.Second

	config := &Config{
		BaseDir:          baseDir,
		WatchDirs:        watchDirs,
		RcloneRemoteName: remoteName,
		CopyParams:       copyParams,
		MaxRetries:       maxRetries,
		RetryDelay:       retryDelay,
		UploadTimeout:    uploadTimeout,
		DBTimeout:        dbTimeout,
	}

	return config, nil
}

// getEnvWithDefault gets an environment variable or returns the default value
func getEnvWithDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// getEnvIntWithDefault gets an environment variable as int or returns the default value
func getEnvIntWithDefault(key string, defaultValue int) int {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}

	value, err := strconv.Atoi(valueStr)
	if err != nil || value <= 0 {
		fmt.Printf("Warning: Invalid value for %s, using default: %d\n", key, defaultValue)
		return defaultValue
	}

	return value
}
