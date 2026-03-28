// Package process manages the lifecycle, execution, and output multiplexing of child processes.
package process

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"strings"
)

// pipeLogger consumes lines from an io.ReadCloser and emits them as structured slog entries.
// It maintains process context to avoid log interleaving in the supervisor's output.
func pipeLogger(name, stream string, r io.ReadCloser) {
	// Ensure the pipe is closed to release OS resources when the process exits or the scanner fails.
	defer r.Close() //nolint:errcheck // Best-effort cleanup for pipe reader.

	scanner := bufio.NewScanner(r)

	// Increase buffer size to 64KB to handle potentially long AdGuard Home log entries
	// without triggering bufio.ErrTooLong.
	const maxLogLine = 64 * 1024
	buf := make([]byte, maxLogLine)
	scanner.Buffer(buf, maxLogLine)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		level := slog.LevelInfo
		if stream == "stderr" {
			// Map streams to logical severity levels. Stderr defaults to warning.
			// Specific markers in stderr are parsed to correctly classify the entry.
			level = slog.LevelWarn
			lowerLine := strings.ToLower(line)
			switch {
			case strings.Contains(lowerLine, "[error]"):
				level = slog.LevelError
			case strings.Contains(lowerLine, "[warn]"):
				level = slog.LevelWarn
			case strings.Contains(lowerLine, "[info]"):
				level = slog.LevelInfo
			}
		}

		slog.Log(context.Background(), level, line,
			slog.String("process", name),
			slog.String("stream", stream),
		)
	}

	if err := scanner.Err(); err != nil {
		slog.Debug("Log pipe closed",
			"process", name,
			"stream", stream,
			"error", err,
		)
	}
}
