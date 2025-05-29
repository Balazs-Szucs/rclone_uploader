package errors

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"

	"rclone_uploader/internal/logging"
)

// Custom error types
type ErrorType string

const (
	ErrorTypeValidation    ErrorType = "VALIDATION"
	ErrorTypeUpload        ErrorType = "UPLOAD"
	ErrorTypeSystem        ErrorType = "SYSTEM"
	ErrorTypePermission    ErrorType = "PERMISSION"
	ErrorTypeConfiguration ErrorType = "CONFIGURATION"
)

// AppError represents an application-specific error
type AppError struct {
	Type    ErrorType
	Message string
	Err     error
	Stack   string
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s (%s)", e.Type, e.Message, e.Err.Error())
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// NewError creates a new AppError
func NewError(errType ErrorType, message string, err error) *AppError {
	return &AppError{
		Type:    errType,
		Message: message,
		Err:     err,
		Stack:   string(debug.Stack()),
	}
}

// Error response structure
type ErrorResponse struct {
	Status  int       `json:"status"`
	Message string    `json:"message"`
	Type    ErrorType `json:"type"`
	Details string    `json:"details,omitempty"`
}

// HTTP error handler middleware
func ErrorHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				logger := logging.Logger()
				logger.Errorw("panic recovered",
					"error", err,
					"stack", string(debug.Stack()),
				)

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintf(w, `{"status":500,"message":"Internal server error","type":"SYSTEM"}`)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// Error to HTTP status code mapping
func statusCodeForError(err error) int {
	if appErr, ok := err.(*AppError); ok {
		switch appErr.Type {
		case ErrorTypeValidation:
			return http.StatusBadRequest
		case ErrorTypePermission:
			return http.StatusForbidden
		case ErrorTypeUpload:
			return http.StatusBadGateway
		case ErrorTypeConfiguration:
			return http.StatusUnprocessableEntity
		default:
			return http.StatusInternalServerError
		}
	}
	return http.StatusInternalServerError
}

// Validation error helpers
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func NewValidationError(field, message string) *AppError {
	return NewError(ErrorTypeValidation, fmt.Sprintf("Validation error: %s", message), nil)
}

// Upload error helpers
func NewUploadError(message string, err error) *AppError {
	return NewError(ErrorTypeUpload, message, err)
}

// Permission error helpers
func NewPermissionError(message string) *AppError {
	return NewError(ErrorTypePermission, message, nil)
}

// Configuration error helpers
func NewConfigError(message string, err error) *AppError {
	return NewError(ErrorTypeConfiguration, message, err)
}

// Error recovery middleware with logging
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				logger := logging.Logger()
				stack := debug.Stack()

				// Log the error
				logger.Errorw("panic recovered in request handler",
					"error", err,
					"stack", string(stack),
					"url", r.URL.String(),
					"method", r.Method,
					"remote_addr", r.RemoteAddr,
				)

				// Clean stack trace for response
				cleanStack := cleanStackTrace(string(stack))

				// Return error response
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintf(w, `{"status":500,"message":"Internal server error","type":"SYSTEM","details":%q}`,
					cleanStack)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// cleanStackTrace removes sensitive information from stack traces
func cleanStackTrace(stack string) string {
	lines := strings.Split(stack, "\n")
	var cleaned []string
	for _, line := range lines {
		// Remove full paths
		if strings.Contains(line, "/home/") || strings.Contains(line, "/usr/") {
			continue
		}
		// Keep only relevant information
		if strings.Contains(line, ".go:") {
			parts := strings.Split(line, "/")
			if len(parts) > 0 {
				cleaned = append(cleaned, parts[len(parts)-1])
			}
		}
	}
	return strings.Join(cleaned, "\n")
}

// RetryableError interface for errors that can be retried
type RetryableError interface {
	error
	CanRetry() bool
	MaxRetries() int
}

// RetryableUploadError represents a retryable upload error
type RetryableUploadError struct {
	*AppError
	retries    int
	maxRetries int
}

func NewRetryableUploadError(message string, err error, maxRetries int) *RetryableUploadError {
	return &RetryableUploadError{
		AppError:   NewError(ErrorTypeUpload, message, err),
		maxRetries: maxRetries,
	}
}

func (e *RetryableUploadError) CanRetry() bool {
	return e.retries < e.maxRetries
}

func (e *RetryableUploadError) MaxRetries() int {
	return e.maxRetries
}

func (e *RetryableUploadError) IncrementRetry() {
	e.retries++
}

// SendError sends an error response to the client
func SendError(w http.ResponseWriter, err *AppError) {
	logger := logging.Logger()
	logger.Errorw("error occurred",
		"error", err.Error(),
		"type", err.Type,
		"stack", err.Stack,
	)

	response := ErrorResponse{
		Status:  statusCodeForError(err),
		Message: err.Message,
		Type:    err.Type,
	}

	if err.Err != nil {
		response.Details = err.Err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.Status)
	json.NewEncoder(w).Encode(response)
}
