package logging

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/Scottlr/nudge/internal/app"
)

// Logger is the only public slog-backed logger. Callers can submit only the
// application-owned event and SafeField vocabulary.
type Logger struct {
	base      *slog.Logger
	writer    *Writer
	health    *healthState
	now       func() time.Time
	expiresAt time.Time
}

// New creates a bounded protected operational logger. If the sink cannot be
// created, the returned logger is disabled and exposes a redacted health
// counter; logging failure never becomes review-state failure.
func New(ctx context.Context, config Config) *Logger {
	return newLogger(ctx, config, time.Time{})
}

// NewDebug creates a separate, tighter, time-limited sink. It shares the
// closed event/field boundary with the normal logger; debug never admits raw
// source, prompts, patches, protocol, credentials, or runtime arguments.
func NewDebug(ctx context.Context, config Config, until time.Time) *Logger {
	if config.Now == nil {
		config.Now = time.Now
	}
	now := config.Now()
	if until.IsZero() || !until.After(now) {
		return disabledLogger(app.LogFailureExpired)
	}
	if until.Sub(now) > 24*time.Hour {
		until = now.Add(24 * time.Hour)
	}
	config.Root = filepath.Join(config.Root, "debug")
	if config.MaxBytes == 0 || config.MaxBytes > 1*1024*1024 {
		config.MaxBytes = 1 * 1024 * 1024
	}
	if config.MaxFiles == 0 || config.MaxFiles > 2 {
		config.MaxFiles = 2
	}
	if config.Retention == 0 || config.Retention > time.Hour {
		config.Retention = time.Hour
	}
	return newLogger(ctx, config, until)
}

func newLogger(ctx context.Context, config Config, expiresAt time.Time) *Logger {
	health := &healthState{}
	writer, err := NewWriter(ctx, config, health)
	if err != nil {
		return disabledLoggerWithHealth(health, app.LogFailureOpen)
	}
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: config.Level, AddSource: false})
	base := slog.New(handler).With(
		slog.Uint64("schema_version", uint64(logSchemaVersion)),
		slog.String("process_id", writer.ProcessID()),
	)
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Logger{base: base, writer: writer, health: health, now: now, expiresAt: expiresAt}
}

func disabledLogger(code app.LogFailureCode) *Logger {
	return disabledLoggerWithHealth(&healthState{}, code)
}

func disabledLoggerWithHealth(health *healthState, code app.LogFailureCode) *Logger {
	health.fail(code)
	return &Logger{health: health}
}

// Log records one safe event. Invalid event or field input is discarded and
// increments only the bounded redacted sink-health state.
func (l *Logger) Log(ctx context.Context, code app.LogEventCode, fields ...app.SafeField) {
	if l == nil || l.base == nil || l.writer == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !l.expiresAt.IsZero() && !l.now().Before(l.expiresAt) {
		l.health.fail(app.LogFailureExpired)
		return
	}
	if err := app.ValidateEventCode(code); err != nil {
		l.health.reject()
		return
	}
	attrs := make([]slog.Attr, 0, len(fields))
	for _, field := range fields {
		if err := field.Validate(); err != nil {
			l.health.reject()
			return
		}
		attrs = append(attrs, field.Attr())
	}
	l.base.LogAttrs(ctx, app.EventLevel(code), string(code), attrs...)
}

// Health returns only bounded sink status and a stable failure code.
func (l *Logger) Health() app.LogHealth {
	if l == nil {
		return app.LogHealth{}
	}
	return l.health.snapshot()
}

// Close flushes and closes the current owner-marked sink.
func (l *Logger) Close() error {
	if l == nil || l.writer == nil {
		return nil
	}
	return l.writer.Close()
}

var _ app.OperationalLogger = (*Logger)(nil)

// IsDisabled reports the typed sink-failure outcome without exposing a raw
// filesystem or OS error to operational callers.
func IsDisabled(err error) bool {
	return errors.Is(err, errLogDisabled)
}
