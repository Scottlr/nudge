package app

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
)

// LogEventCode is the closed operational-event vocabulary. Event messages
// are identifiers, never caller-supplied prose.
type LogEventCode string

const (
	LogEventProcessStarted    LogEventCode = "process.started"
	LogEventProcessFinished   LogEventCode = "process.finished"
	LogEventOperationStarted  LogEventCode = "operation.started"
	LogEventOperationFinished LogEventCode = "operation.finished"
	LogEventTargetRefreshed   LogEventCode = "target.refreshed"
	LogEventProviderState     LogEventCode = "provider.state"
	LogEventReconciliation    LogEventCode = "reconciliation.completed"
	LogEventWorkspaceState    LogEventCode = "workspace.state"
	LogEventRepairPlan        LogEventCode = "repair.plan"
	LogEventHealth            LogEventCode = "health.observed"
	LogEventSinkDisabled      LogEventCode = "log.sink_disabled"
)

// LogDetailCode is the closed failure/outcome vocabulary admitted as a field.
type LogDetailCode string

const (
	LogDetailCompleted       LogDetailCode = "completed"
	LogDetailFailed          LogDetailCode = "failed"
	LogDetailCanceled        LogDetailCode = "canceled"
	LogDetailTimeout         LogDetailCode = "timeout"
	LogDetailUnavailable     LogDetailCode = "unavailable"
	LogDetailRejected        LogDetailCode = "rejected"
	LogDetailStale           LogDetailCode = "stale"
	LogDetailOverflow        LogDetailCode = "overflow"
	LogDetailQueryOnly       LogDetailCode = "query_only"
	LogDetailCapacityLimited LogDetailCode = "capacity_limited"
)

// LogFieldKey identifies a safe operational attribute without accepting an
// arbitrary caller-defined key.
type LogFieldKey string

const (
	LogFieldOperationID          LogFieldKey = "operation_id"
	LogFieldRepositoryID         LogFieldKey = "repository_id"
	LogFieldWorktreeID           LogFieldKey = "worktree_id"
	LogFieldSessionID            LogFieldKey = "session_id"
	LogFieldThreadID             LogFieldKey = "thread_id"
	LogFieldProposalID           LogFieldKey = "proposal_id"
	LogFieldWorkspaceID          LogFieldKey = "workspace_id"
	LogFieldCaptureID            LogFieldKey = "capture_id"
	LogFieldSnapshotID           LogFieldKey = "snapshot_id"
	LogFieldProviderConversation LogFieldKey = "provider_conversation_id"
	LogFieldProviderTurn         LogFieldKey = "provider_turn_id"
	LogFieldCount                LogFieldKey = "count"
	LogFieldBytes                LogFieldKey = "bytes"
	LogFieldDuration             LogFieldKey = "duration"
	LogFieldVersion              LogFieldKey = "version"
	LogFieldDetailCode           LogFieldKey = "detail_code"
	LogFieldRetryable            LogFieldKey = "retryable"
	LogFieldEvidence             LogFieldKey = "evidence"
)

const (
	maxLogIdentifierBytes = 256
	maxLogEvidenceBytes   = 512
	maxLogCount           = uint64(1) << 40
	maxLogBytes           = uint64(1) << 50
	maxLogDuration        = 24 * time.Hour
)

var ErrInvalidLogField = errors.New("invalid operational log field")

type logFieldKind uint8

const (
	logStringField logFieldKind = iota + 1
	logUintField
	logDurationField
	logBoolField
)

// SafeField is a typed operational attribute. Its constructors admit only
// Nudge IDs, bounded quantities, closed codes, booleans, and pre-redacted
// evidence; there is no arbitrary value or raw error constructor.
type SafeField struct {
	key      LogFieldKey
	kind     logFieldKind
	text     string
	quantity uint64
	duration time.Duration
	boolean  bool
}

// Validate checks that a field was produced by one of the safe constructors.
func (f SafeField) Validate() error {
	if !validLogFieldKey(f.key) {
		return ErrInvalidLogField
	}
	switch f.kind {
	case logStringField:
		if f.text == "" || len([]byte(f.text)) > maxLogEvidenceBytes || !safeLogText(f.text) {
			return ErrInvalidLogField
		}
	case logUintField:
		if f.key == LogFieldCount && f.quantity > maxLogCount || f.key == LogFieldBytes && f.quantity > maxLogBytes || f.key == LogFieldVersion && f.quantity > math.MaxUint32 {
			return ErrInvalidLogField
		}
	case logDurationField:
		if f.duration < 0 || f.duration > maxLogDuration {
			return ErrInvalidLogField
		}
	case logBoolField:
	default:
		return ErrInvalidLogField
	}
	return nil
}

// Attr converts a validated field into the standard-library slog value used
// by the protected handler. Invalid zero values become an empty attribute and
// are rejected by the logging boundary before handling.
func (f SafeField) Attr() slog.Attr {
	switch f.kind {
	case logStringField:
		return slog.String(string(f.key), f.text)
	case logUintField:
		return slog.Uint64(string(f.key), f.quantity)
	case logDurationField:
		return slog.Duration(string(f.key), f.duration)
	case logBoolField:
		return slog.Bool(string(f.key), f.boolean)
	default:
		return slog.Attr{}
	}
}

// FieldOperationID constructs an operation identity field.
func FieldOperationID(value domain.OperationID) (SafeField, error) {
	return stringField(LogFieldOperationID, string(value), maxLogIdentifierBytes)
}

// FieldRepositoryID constructs a repository identity field.
func FieldRepositoryID(value domain.RepositoryID) (SafeField, error) {
	return stringField(LogFieldRepositoryID, string(value), maxLogIdentifierBytes)
}

// FieldWorktreeID constructs a worktree identity field.
func FieldWorktreeID(value domain.WorktreeID) (SafeField, error) {
	return stringField(LogFieldWorktreeID, string(value), maxLogIdentifierBytes)
}

// FieldSessionID constructs a review-session identity field.
func FieldSessionID(value domain.ReviewSessionID) (SafeField, error) {
	return stringField(LogFieldSessionID, string(value), maxLogIdentifierBytes)
}

// FieldThreadID constructs a review-thread identity field.
func FieldThreadID(value domain.ReviewThreadID) (SafeField, error) {
	return stringField(LogFieldThreadID, string(value), maxLogIdentifierBytes)
}

// FieldProposalID constructs a proposal identity field.
func FieldProposalID(value domain.ProposalID) (SafeField, error) {
	return stringField(LogFieldProposalID, string(value), maxLogIdentifierBytes)
}

// FieldWorkspaceID constructs a proposal-workspace identity field.
func FieldWorkspaceID(value domain.WorkspaceID) (SafeField, error) {
	return stringField(LogFieldWorkspaceID, string(value), maxLogIdentifierBytes)
}

// FieldCaptureID constructs an immutable-capture identity field.
func FieldCaptureID(value domain.CaptureID) (SafeField, error) {
	return stringField(LogFieldCaptureID, string(value), maxLogIdentifierBytes)
}

// FieldSnapshotID constructs an immutable-snapshot identity field.
func FieldSnapshotID(value domain.ReviewSnapshotID) (SafeField, error) {
	return stringField(LogFieldSnapshotID, string(value), maxLogIdentifierBytes)
}

// FieldProviderConversationID constructs a Nudge-local conversation identity field.
func FieldProviderConversationID(value domain.ProviderConversationID) (SafeField, error) {
	return stringField(LogFieldProviderConversation, string(value), maxLogIdentifierBytes)
}

// FieldProviderTurnID constructs a Nudge-local turn identity field.
func FieldProviderTurnID(value domain.ProviderTurnID) (SafeField, error) {
	return stringField(LogFieldProviderTurn, string(value), maxLogIdentifierBytes)
}

// FieldCount constructs a bounded count field.
func FieldCount(value Count) (SafeField, error) {
	return quantityField(LogFieldCount, uint64(value))
}

// FieldBytes constructs a bounded byte-size field.
func FieldBytes(value ByteSize) (SafeField, error) {
	return quantityField(LogFieldBytes, uint64(value))
}

// FieldDuration constructs a bounded duration field.
func FieldDuration(value time.Duration) (SafeField, error) {
	field := SafeField{key: LogFieldDuration, kind: logDurationField, duration: value}
	return field, field.Validate()
}

// FieldVersion constructs a bounded policy/version field.
func FieldVersion(value uint32) (SafeField, error) {
	return quantityField(LogFieldVersion, uint64(value))
}

// FieldDetailCode constructs a closed outcome-code field.
func FieldDetailCode(value LogDetailCode) (SafeField, error) {
	if !validLogDetailCode(value) {
		return SafeField{}, ErrInvalidLogField
	}
	return stringField(LogFieldDetailCode, string(value), maxLogIdentifierBytes)
}

// FieldRetryable constructs a retryability field.
func FieldRetryable(value bool) SafeField {
	return SafeField{key: LogFieldRetryable, kind: logBoolField, boolean: value}
}

// FieldEvidence constructs a bounded pre-redacted evidence field.
func FieldEvidence(value string) (SafeField, error) {
	return stringField(LogFieldEvidence, value, maxLogEvidenceBytes)
}

func stringField(key LogFieldKey, value string, limit int) (SafeField, error) {
	if len([]byte(value)) == 0 || len([]byte(value)) > limit || !safeLogText(value) {
		return SafeField{}, ErrInvalidLogField
	}
	field := SafeField{key: key, kind: logStringField, text: value}
	return field, field.Validate()
}

func quantityField(key LogFieldKey, value uint64) (SafeField, error) {
	field := SafeField{key: key, kind: logUintField, quantity: value}
	return field, field.Validate()
}

func validLogFieldKey(key LogFieldKey) bool {
	switch key {
	case LogFieldOperationID, LogFieldRepositoryID, LogFieldWorktreeID, LogFieldSessionID,
		LogFieldThreadID, LogFieldProposalID, LogFieldWorkspaceID, LogFieldCaptureID,
		LogFieldSnapshotID, LogFieldProviderConversation, LogFieldProviderTurn,
		LogFieldCount, LogFieldBytes, LogFieldDuration, LogFieldVersion, LogFieldDetailCode,
		LogFieldRetryable, LogFieldEvidence:
		return true
	default:
		return false
	}
}

func validLogDetailCode(code LogDetailCode) bool {
	switch code {
	case LogDetailCompleted, LogDetailFailed, LogDetailCanceled, LogDetailTimeout,
		LogDetailUnavailable, LogDetailRejected, LogDetailStale, LogDetailOverflow,
		LogDetailQueryOnly, LogDetailCapacityLimited:
		return true
	default:
		return false
	}
}

func validLogEventCode(code LogEventCode) bool {
	switch code {
	case LogEventProcessStarted, LogEventProcessFinished, LogEventOperationStarted,
		LogEventOperationFinished, LogEventTargetRefreshed, LogEventProviderState,
		LogEventReconciliation, LogEventWorkspaceState, LogEventRepairPlan,
		LogEventHealth, LogEventSinkDisabled:
		return true
	default:
		return false
	}
}

func safeLogText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r == 0 || unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return true
}

// LogHealth is a bounded, redacted sink-health snapshot.
type LogHealth struct {
	Disabled     bool
	FailureCount Count
	LastFailure  LogFailureCode
}

// LogFailureCode identifies why one sink stopped accepting records.
type LogFailureCode string

const (
	LogFailureOpen      LogFailureCode = "open_failed"
	LogFailureWrite     LogFailureCode = "write_failed"
	LogFailureRotation  LogFailureCode = "rotation_failed"
	LogFailureRetention LogFailureCode = "retention_failed"
	LogFailureCapacity  LogFailureCode = "capacity_exceeded"
	LogFailureRejected  LogFailureCode = "field_rejected"
	LogFailureExpired   LogFailureCode = "debug_expired"
)

// OperationalLogger is the application-owned logging port. Logging failure
// is observable through Health and never returned as a workflow error.
type OperationalLogger interface {
	Log(context.Context, LogEventCode, ...SafeField)
	Health() LogHealth
	Close() error
}

// NopOperationalLogger is the zero-effect logger for no-persist or tests.
type NopOperationalLogger struct{}

func (NopOperationalLogger) Log(context.Context, LogEventCode, ...SafeField) {}
func (NopOperationalLogger) Health() LogHealth                               { return LogHealth{} }
func (NopOperationalLogger) Close() error                                    { return nil }

var _ OperationalLogger = NopOperationalLogger{}

// ValidateEventCode is used by platform logging adapters without exposing
// the closed vocabulary's implementation details.
func ValidateEventCode(code LogEventCode) error {
	if !validLogEventCode(code) {
		return ErrInvalidLogField
	}
	return nil
}

// EventLevel maps the stable event class to slog's level vocabulary.
func EventLevel(code LogEventCode) slog.Level {
	if code == LogEventHealth || code == LogEventSinkDisabled {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

// NormalizeLogEvidence keeps an owner-provided already-redacted scalar within
// the sink bound without accepting control-bearing text.
func NormalizeLogEvidence(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len([]byte(value)) > maxLogEvidenceBytes || !safeLogText(value) || value == "" {
		return "", ErrInvalidLogField
	}
	return value, nil
}
