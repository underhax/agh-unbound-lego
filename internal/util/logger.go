package util

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"strings"
)

// LevelParser determines the slog level for a given log line.
// If nil, the default level passed to StreamToLog is used for every line.
type LevelParser func(line string) slog.Level

// StreamToLog reads lines from r and emits them as structured slog entries at the given level.
// Closes r on return to release OS file descriptors.
// If parser is non-nil, it is called per line to dynamically override the default level.
func StreamToLog(ctx context.Context, name, stream string, r io.ReadCloser, level slog.Level, parser LevelParser) {
	defer r.Close() //nolint:errcheck // Best-effort cleanup for pipe reader.

	scanner := bufio.NewScanner(r)

	// Sized to handle long AdGuard Home log entries without triggering bufio.ErrTooLong.
	const maxLogLine = 64 * 1024
	buf := make([]byte, maxLogLine)
	scanner.Buffer(buf, maxLogLine)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		currentLevel := level
		if parser != nil {
			currentLevel = parser(line)
		}

		slog.Log(ctx, currentLevel, line,
			slog.String("process", name),
			slog.String("stream", stream),
		)
	}

	if err := scanner.Err(); err != nil {
		slog.Debug("Log pipe closed", "process", name, "stream", stream, "error", err)
	}
}
