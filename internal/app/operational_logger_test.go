package app

import (
	"context"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

func TestSafeOperationalFieldsRejectInvalidAndSensitiveShapes(t *testing.T) {
	if _, err := FieldOperationID(domain.OperationID("")); err == nil {
		t.Fatal("empty operation ID unexpectedly accepted")
	}
	if _, err := FieldEvidence("source\x1b[31m"); err == nil {
		t.Fatal("control-bearing evidence unexpectedly accepted")
	}
	if _, err := FieldDetailCode(LogDetailCode("raw-provider-payload")); err == nil {
		t.Fatal("unknown detail code unexpectedly accepted")
	}
	if _, err := FieldDuration(25 * time.Hour); err == nil {
		t.Fatal("unbounded duration unexpectedly accepted")
	}
	field, err := FieldOperationID(domain.OperationID("operation-1"))
	if err != nil || field.Validate() != nil {
		t.Fatalf("valid operation field = %#v, error = %v", field, err)
	}
}

func TestOperationalLoggerPortHasNoEffectInNopMode(t *testing.T) {
	var logger OperationalLogger = NopOperationalLogger{}
	logger.Log(context.TODO(), LogEventOperationFinished)
	if logger.Health() != (LogHealth{}) {
		t.Fatalf("nop logger health = %#v", logger.Health())
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("nop logger close error = %v", err)
	}
}

func TestClientRoutesOperationLifecycleToLogger(t *testing.T) {
	logger := &recordingOperationalLogger{}
	client, err := NewClient(ClientOptions{Logger: logger})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()
	if _, err := client.Dispatch(context.Background(), OpenRepository{Path: "repository", CorrelationID: "open-1"}); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if len(logger.events) != 1 || logger.events[0] != LogEventOperationStarted {
		t.Fatalf("logged operation events = %#v", logger.events)
	}
}

type recordingOperationalLogger struct {
	events []LogEventCode
}

func (l *recordingOperationalLogger) Log(_ context.Context, code LogEventCode, _ ...SafeField) {
	l.events = append(l.events, code)
}

func (l *recordingOperationalLogger) Health() LogHealth { return LogHealth{} }
func (l *recordingOperationalLogger) Close() error      { return nil }
