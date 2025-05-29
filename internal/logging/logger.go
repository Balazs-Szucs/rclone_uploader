package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	logger     *zap.SugaredLogger
	loggerOnce sync.Once
	loggerMu   sync.RWMutex
)

type LogConfig struct {
	LogDir      string
	MaxSize     int
	MaxBackups  int
	MaxAge      int
	Compress    bool
	Development bool
}

// DefaultConfig returns the default logging configuration
func DefaultConfig() LogConfig {
	return LogConfig{
		LogDir:      "logs",
		MaxSize:     100,   // 100MB
		MaxBackups:  5,     // Keep 5 backups
		MaxAge:      30,    // 30 days
		Compress:    true,  // Compress old logs
		Development: false, // Production mode by default
	}
}

// InitLogger initializes the global logger with the given configuration.
// If called multiple times, only the first call will take effect.
func InitLogger(config LogConfig) error {
	var err error
	loggerOnce.Do(func() {
		err = initLogger(config)
	})
	return err
}

// initLogger is the actual initialization function
func initLogger(config LogConfig) error {
	// Create logs directory if it doesn't exist
	if err := os.MkdirAll(config.LogDir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %v", err)
	}

	// Configure log rotation
	logWriter := &lumberjack.Logger{
		Filename:   filepath.Join(config.LogDir, "rclone_uploader.log"),
		MaxSize:    config.MaxSize,
		MaxBackups: config.MaxBackups,
		MaxAge:     config.MaxAge,
		Compress:   config.Compress,
	}

	// Configure encoder
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// Create core
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.NewMultiWriteSyncer(
			zapcore.AddSync(os.Stdout),
			zapcore.AddSync(logWriter),
		),
		zap.NewAtomicLevelAt(zapcore.InfoLevel),
	)

	// Create logger
	zapOpts := []zap.Option{
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	}
	if config.Development {
		zapOpts = append(zapOpts, zap.Development())
	}
	zapLogger := zap.New(core, zapOpts...)

	loggerMu.Lock()
	logger = zapLogger.Sugar()
	loggerMu.Unlock()

	return nil
}

// Logger returns the global logger instance.
// If the logger hasn't been initialized, it will be initialized with default config.
func Logger() *zap.SugaredLogger {
	loggerMu.RLock()
	l := logger
	loggerMu.RUnlock()

	if l == nil {
		if err := InitLogger(DefaultConfig()); err != nil {
			// If we can't initialize the logger, create a basic console logger
			l = zap.NewExample().Sugar()
		}
		loggerMu.RLock()
		l = logger
		loggerMu.RUnlock()
	}

	return l
}

// Shutdown gracefully shuts down the logger
func Shutdown() error {
	loggerMu.Lock()
	defer loggerMu.Unlock()

	if logger != nil {
		return logger.Sync()
	}
	return nil
}

// Structured logging helpers
func WithFields(fields map[string]interface{}) *zap.SugaredLogger {
	args := make([]interface{}, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	return Logger().With(args...)
}

// Metrics logging
type MetricsLogger struct {
	logger *zap.SugaredLogger
}

func NewMetricsLogger() *MetricsLogger {
	return &MetricsLogger{
		logger: Logger().Named("metrics"),
	}
}

func (m *MetricsLogger) LogUploadMetrics(metrics map[string]interface{}) {
	m.logger.Infow("upload_metrics",
		"timestamp", time.Now().Unix(),
		"metrics", metrics,
	)
}

func (m *MetricsLogger) LogError(err error, msg string) {
	_, file, line, _ := runtime.Caller(1)
	m.logger.Errorw(msg,
		"error", err,
		"file", file,
		"line", line,
	)
}

// Performance logging
type PerformanceLogger struct {
	logger *zap.SugaredLogger
}

func NewPerformanceLogger() *PerformanceLogger {
	return &PerformanceLogger{
		logger: Logger().Named("performance"),
	}
}

func (p *PerformanceLogger) LogDuration(operation string, duration time.Duration) {
	p.logger.Infow("operation_duration",
		"operation", operation,
		"duration_ms", duration.Milliseconds(),
	)
}

func (p *PerformanceLogger) StartOperation(operation string) func() {
	start := time.Now()
	return func() {
		p.LogDuration(operation, time.Since(start))
	}
}

// Security logging
type SecurityLogger struct {
	logger *zap.SugaredLogger
}

func NewSecurityLogger() *SecurityLogger {
	return &SecurityLogger{
		logger: Logger().Named("security"),
	}
}

func (s *SecurityLogger) LogAccessAttempt(success bool, ip string, method string, path string) {
	s.logger.Infow("access_attempt",
		"success", success,
		"ip", ip,
		"method", method,
		"path", path,
		"timestamp", time.Now().Unix(),
	)
}

func (s *SecurityLogger) LogSecurityEvent(eventType string, details map[string]interface{}) {
	args := []interface{}{
		"event_type", eventType,
		"timestamp", time.Now().Unix(),
	}
	for k, v := range details {
		args = append(args, k, v)
	}
	s.logger.Warnw("security_event", args...)
}
