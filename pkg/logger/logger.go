package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Logger wraps zap logger
type Logger struct {
	*zap.Logger
	level zapcore.Level
	close func() error
}

// Config represents logger configuration
type Config struct {
	Level      string `yaml:"level"`
	LogDir     string `yaml:"log_dir"`
	MaxSize    int    `yaml:"max_size"`    // megabytes
	MaxBackups int    `yaml:"max_backups"` // number of backups
	MaxAge     int    `yaml:"max_age"`     // days
}

var (
	defaultMu     sync.RWMutex
	defaultLogger *Logger
)

// New creates a new logger
func New(cfg *Config) (*Logger, error) {
	// Parse log level
	var level zapcore.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = zapcore.DebugLevel
	case "info":
		level = zapcore.InfoLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	default:
		level = zapcore.InfoLevel
	}

	// Create log directory if it doesn't exist
	logDir := cfg.LogDir
	if logDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get user home directory: %w", err)
		}
		logDir = filepath.Join(homeDir, ".local", "state", "inferoute", "log")
	}

	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Create encoder config
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	fileEncoder := zapcore.NewJSONEncoder(encoderConfig)

	// Setup log rotation using lumberjack
	mainLogFile := &lumberjack.Logger{
		Filename:   filepath.Join(logDir, "inferoute.log"),
		MaxSize:    cfg.MaxSize,    // megabytes
		MaxBackups: cfg.MaxBackups, // number of backups
		MaxAge:     cfg.MaxAge,     // days
		Compress:   true,           // compress old logs
	}

	errorLogFile := &lumberjack.Logger{
		Filename:   filepath.Join(logDir, "error.log"),
		MaxSize:    cfg.MaxSize,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAge,
		Compress:   true,
	}

	fileCore := zapcore.NewCore(
		fileEncoder,
		zapcore.AddSync(mainLogFile),
		level,
	)

	errorCore := zapcore.NewCore(
		fileEncoder,
		zapcore.AddSync(errorLogFile),
		zapcore.ErrorLevel,
	)

	// Files only. The terminal UI is status and inference requests; failures stay in the logs.
	core := zapcore.NewTee(fileCore, errorCore)
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	return &Logger{
		Logger: logger,
		level:  level,
		close: func() error {
			err := mainLogFile.Close()
			if e := errorLogFile.Close(); err == nil {
				err = e
			}
			return err
		},
	}, nil
}

// Close releases the log files. Windows cannot delete a file that is still open.
func (l *Logger) Close() error {
	if l == nil || l.close == nil {
		return nil
	}
	return l.close()
}

// DefaultConfig is the logging main installs before it has read the config
// file: info level, files under ~/.local/state/inferoute/log.
func DefaultConfig() *Config {
	return &Config{Level: "info", MaxSize: 100, MaxBackups: 5, MaxAge: 30}
}

// nop is the default until main installs a logger with SetDefaultLogger.
var nop = &Logger{Logger: zap.NewNop(), level: zapcore.InfoLevel}

// GetDefaultLogger returns the logger main installed, or one that discards
// everything if none was. A library or a test that logs before main runs
// therefore writes no file (finding TST-1).
func GetDefaultLogger() *Logger {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	if defaultLogger != nil {
		return defaultLogger
	}
	return nop
}

// SetDefaultLogger sets the default logger
func SetDefaultLogger(logger *Logger) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultLogger = logger
}

// Debug logs a debug message
func Debug(msg string, fields ...zapcore.Field) {
	GetDefaultLogger().Debug(msg, fields...)
}

// Info logs an info message
func Info(msg string, fields ...zapcore.Field) {
	GetDefaultLogger().Info(msg, fields...)
}

// Warn logs a warning message
func Warn(msg string, fields ...zapcore.Field) {
	GetDefaultLogger().Warn(msg, fields...)
}

// Error logs an error message
func Error(msg string, fields ...zapcore.Field) {
	GetDefaultLogger().Error(msg, fields...)
}

// Fatal logs a fatal message and exits
func Fatal(msg string, fields ...zapcore.Field) {
	GetDefaultLogger().Fatal(msg, fields...)
}
