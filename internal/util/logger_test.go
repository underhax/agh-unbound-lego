package util

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

type errReader struct {
	err error
}

func (e *errReader) Read(_ []byte) (int, error) {
	return 0, e.err
}

func (e *errReader) Close() error {
	return nil
}

type errCloser struct {
	io.Reader
	closeErr error
}

func (e *errCloser) Close() error {
	return e.closeErr
}

func TestStreamToLog(t *testing.T) {
	tests := []struct {
		reader io.ReadCloser
		parser LevelParser
		name   string
		level  slog.Level
	}{
		{
			name:   "StandardLines",
			reader: io.NopCloser(strings.NewReader("first line\n\n  \nsecond line\n")),
			level:  slog.LevelInfo,
			parser: nil,
		},
		{
			name:   "WithCustomParser",
			reader: io.NopCloser(strings.NewReader("warn: something bad\ninfo: ok\n")),
			level:  slog.LevelInfo,
			parser: func(line string) slog.Level {
				if strings.HasPrefix(line, "warn:") {
					return slog.LevelWarn
				}
				return slog.LevelInfo
			},
		},
		{
			name: "ReaderError",
			reader: &errReader{
				err: errors.New("pipe read error"),
			},
			level: slog.LevelError,
		},
		{
			name: "CloserError",
			reader: &errCloser{
				Reader:   strings.NewReader("content\n"),
				closeErr: errors.New("close error"),
			},
			level: slog.LevelDebug,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(_ *testing.T) {
			StreamToLog(context.Background(), "proc", "stdout", tc.reader, tc.level, tc.parser)
		})
	}
}
