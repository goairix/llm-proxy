package logger

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	rotateLogs "github.com/lestrrat-go/file-rotatelogs"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/goairix/llm-proxy/internal/config"
)

// New creates a zap logger that writes to both stdout and a rotating log file.
// Log file rotates daily via file-rotatelogs, retaining logs for cfg.MaxAge days.
// If cfg.File is empty, only stdout is used.
func New(cfg config.LogConfig) (*zap.Logger, error) {
	level := parseLevel(cfg.Level)

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "timestamp"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	consoleEncoder := zapcore.NewConsoleEncoder(encoderCfg)
	jsonEncoder := zapcore.NewJSONEncoder(encoderCfg)

	var core zapcore.Core

	if cfg.File != "" {
		fileWriter, err := logFileWriter(cfg)
		if err != nil {
			return nil, err
		}
		writeSyncer := zapcore.NewMultiWriteSyncer(zapcore.AddSync(os.Stdout), zapcore.AddSync(fileWriter))
		core = zapcore.NewTee(
			zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), level),
			zapcore.NewCore(jsonEncoder, writeSyncer, level),
		)
	} else {
		core = zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), level)
	}

	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	return logger, nil
}

// logFileWriter creates a rotating file writer using file-rotatelogs.
func logFileWriter(cfg config.LogConfig) (io.Writer, error) {
	ext := filepath.Ext(cfg.File)
	filename := strings.TrimSuffix(cfg.File, ext)
	maxAge := time.Hour * 24 * 30
	if cfg.MaxAge > 0 {
		maxAge = time.Hour * 24 * time.Duration(cfg.MaxAge)
	}
	return rotateLogs.New(
		filename+".%Y-%m-%d"+ext,
		rotateLogs.WithLinkName(cfg.File),
		rotateLogs.WithMaxAge(maxAge),
		rotateLogs.WithRotationTime(time.Hour*24),
	)
}

// parseLevel converts a level string to a zapcore.Level, defaulting to InfoLevel.
func parseLevel(s string) zapcore.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return zapcore.DebugLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
