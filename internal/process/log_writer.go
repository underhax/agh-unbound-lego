// Package process manages the lifecycle, execution, and output multiplexing of child processes.
package process

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"github.com/webstudiobond/agh-unbound-lego/internal/util"
)

// pipeLogger consumes lines from an io.ReadCloser and emits them as structured slog entries.
// It maintains process context to avoid log interleaving in the supervisor's output.
func pipeLogger(name, stream string, r io.ReadCloser) {
	defaultLevel := slog.LevelInfo
	var parser util.LevelParser

	if stream == "stderr" {
		// AGH writes mixed-severity output to stderr. Parse embedded markers to preserve
		// the original severity rather than promoting everything to Warn indiscriminately.
		defaultLevel = slog.LevelWarn
		parser = func(line string) slog.Level {
			lower := strings.ToLower(line)
			switch {
			case strings.Contains(lower, "[error]"):
				return slog.LevelError
			case strings.Contains(lower, "[warn]"):
				return slog.LevelWarn
			case strings.Contains(lower, "[info]"):
				return slog.LevelInfo
			default:
				return slog.LevelWarn
			}
		}
	}

	util.StreamToLog(context.Background(), name, stream, r, defaultLevel, parser)
}
