// Package obs 提供结构化日志与请求可观测性。
package obs

import (
	"fmt"
	"log/slog"
	"os"
)

// New 按级别与格式创建 slog Logger（输出到 stderr）。
func New(level, format string) (*slog.Logger, error) {
	lv, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler
	if format == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	} else {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(h), nil
}

func parseLevel(level string) (slog.Level, error) {
	switch level {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("未知日志级别 %q", level)
	}
}
