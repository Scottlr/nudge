// Package protocol contains the small, version-specific wire boundary used
// by the Codex app-server adapter. These values must not escape this package.
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const methodBytes = 256

var (
	ErrInvalidMessage = errors.New("invalid app-server message")
	ErrInvalidMethod  = errors.New("invalid app-server method")
)

// FrameKind identifies the three app-server envelope forms.
type FrameKind uint8

const (
	FrameResponse FrameKind = iota + 1
	FrameNotification
	FrameServerRequest
)

// RequestID preserves either JSON-RPC request-ID form accepted by the pinned
// app-server schema. Client-generated IDs use NumericRequestID and are
// monotonically increasing; server-generated IDs are echoed byte-for-byte.
type RequestID struct {
	raw    string
	quoted bool
}

// NumericRequestID creates a client-generated numeric request ID.
func NumericRequestID(value uint64) RequestID {
	return RequestID{raw: strconv.FormatUint(value, 10)}
}

// StringRequestID creates a string request ID.
func StringRequestID(value string) (RequestID, error) {
	if !utf8.ValidString(value) {
		return RequestID{}, ErrInvalidMessage
	}
	return RequestID{raw: value, quoted: true}, nil
}

// String returns the unquoted request-ID value for safe comparisons.
func (id RequestID) String() string { return id.raw }

// IsString reports whether the request ID was encoded as a JSON string.
func (id RequestID) IsString() bool { return id.quoted }

func (id RequestID) valid() bool {
	if !utf8.ValidString(id.raw) {
		return false
	}
	if id.quoted {
		return true
	}
	if id.raw == "" {
		return false
	}
	_, err := strconv.ParseInt(id.raw, 10, 64)
	return err == nil
}

func (id RequestID) MarshalJSON() ([]byte, error) {
	if !id.valid() {
		return nil, ErrInvalidMessage
	}
	if id.quoted {
		return json.Marshal(id.raw)
	}
	return []byte(id.raw), nil
}

func (id *RequestID) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return ErrInvalidMessage
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return ErrInvalidMessage
		}
		parsed, err := StringRequestID(value)
		if err != nil {
			return err
		}
		*id = parsed
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return ErrInvalidMessage
	}
	if _, err := strconv.ParseInt(string(number), 10, 64); err != nil {
		return ErrInvalidMessage
	}
	id.raw = string(number)
	id.quoted = false
	return nil
}

// RPCError is the bounded error object returned by the app-server.
type RPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Frame is the parsed JSONL envelope. Only one of Result or Error is present
// for a response; Method is present for a notification or server request.
type Frame struct {
	Kind   FrameKind
	ID     RequestID
	Method string
	Params json.RawMessage
	Result json.RawMessage
	Error  *RPCError
}

// Notification is an additive app-server notification delivered to the
// adapter normalizer.
type Notification struct {
	Method string
	Params json.RawMessage
}

// ServerRequest is a provider-initiated request that requires a response.
type ServerRequest struct {
	ID     RequestID
	Method string
	Params json.RawMessage
}

func validateMethod(method string) error {
	if method == "" || len([]byte(method)) > methodBytes || !utf8.ValidString(method) {
		return ErrInvalidMethod
	}
	for _, r := range method {
		if r == 0 || r == '\r' || r == '\n' || r == '\t' || r < ' ' {
			return ErrInvalidMethod
		}
	}
	return nil
}

func copyRaw(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func validRaw(value json.RawMessage) bool {
	return value != nil && json.Valid(value)
}

// ParseFrame validates and classifies one complete JSONL payload. Additive
// fields are ignored, while ambiguous envelope shapes fail closed.
func ParseFrame(data []byte) (Frame, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Frame{}, ErrInvalidMessage
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return Frame{}, ErrInvalidMessage
	}

	idValue, hasID := fields["id"]
	methodValue, hasMethod := fields["method"]
	paramsValue, hasParams := fields["params"]
	resultValue, hasResult := fields["result"]
	errorValue, hasError := fields["error"]

	var frame Frame
	if hasID {
		if bytes.Equal(bytes.TrimSpace(idValue), []byte("null")) || json.Unmarshal(idValue, &frame.ID) != nil || !frame.ID.valid() {
			return Frame{}, ErrInvalidMessage
		}
	}
	if hasMethod {
		if json.Unmarshal(methodValue, &frame.Method) != nil || validateMethod(frame.Method) != nil {
			return Frame{}, ErrInvalidMessage
		}
	}
	if hasParams {
		if !validRaw(paramsValue) {
			return Frame{}, ErrInvalidMessage
		}
		frame.Params = copyRaw(paramsValue)
	}
	if hasResult {
		if !validRaw(resultValue) {
			return Frame{}, ErrInvalidMessage
		}
		frame.Result = copyRaw(resultValue)
	}
	if hasError {
		if bytes.Equal(bytes.TrimSpace(errorValue), []byte("null")) || json.Unmarshal(errorValue, &frame.Error) != nil || frame.Error == nil || !utf8.ValidString(frame.Error.Message) || len([]byte(frame.Error.Message)) > 64*1024 {
			return Frame{}, ErrInvalidMessage
		}
		if frame.Error.Data != nil && !json.Valid(frame.Error.Data) {
			return Frame{}, ErrInvalidMessage
		}
		frame.Error.Data = copyRaw(frame.Error.Data)
	}

	switch {
	case hasMethod && hasID && !hasResult && !hasError:
		frame.Kind = FrameServerRequest
	case hasMethod && !hasID && !hasResult && !hasError:
		frame.Kind = FrameNotification
	case !hasMethod && hasID && (hasResult != hasError):
		frame.Kind = FrameResponse
	default:
		return Frame{}, ErrInvalidMessage
	}
	if !hasParams {
		frame.Params = nil
	}
	return frame, nil
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		if len(raw) == 0 || !json.Valid(raw) {
			return nil, ErrInvalidMessage
		}
		return copyRaw(raw), nil
	}
	encoded, err := json.Marshal(params)
	if err != nil || !json.Valid(encoded) {
		return nil, ErrInvalidMessage
	}
	return encoded, nil
}

// MarshalRequest encodes a client request without the trailing JSONL newline.
func MarshalRequest(id RequestID, method string, params any) ([]byte, error) {
	if err := validateMethod(method); err != nil {
		return nil, err
	}
	encoded, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	return marshalEnvelope(struct {
		ID     RequestID       `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}{ID: id, Method: method, Params: encoded})
}

// MarshalNotification encodes a client notification without the trailing
// JSONL newline.
func MarshalNotification(method string, params any) ([]byte, error) {
	if err := validateMethod(method); err != nil {
		return nil, err
	}
	encoded, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	return marshalEnvelope(struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}{Method: method, Params: encoded})
}

// MarshalResponse encodes a response to a server request without the trailing
// JSONL newline.
func MarshalResponse(id RequestID, result json.RawMessage, rpcErr *RPCError) ([]byte, error) {
	if (result == nil) == (rpcErr == nil) {
		return nil, ErrInvalidMessage
	}
	if result != nil && !json.Valid(result) {
		return nil, ErrInvalidMessage
	}
	if rpcErr != nil {
		if !utf8.ValidString(rpcErr.Message) || len([]byte(rpcErr.Message)) > 64*1024 {
			return nil, ErrInvalidMessage
		}
	}
	return marshalEnvelope(struct {
		ID     RequestID       `json:"id"`
		Result json.RawMessage `json:"result,omitempty"`
		Error  *RPCError       `json:"error,omitempty"`
	}{ID: id, Result: result, Error: rpcErr})
}

func marshalEnvelope(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal app-server message: %w", err)
	}
	return encoded, nil
}

// MethodName returns a safe method string for diagnostics without exposing
// params or response bodies.
func (f Frame) MethodName() string {
	return strings.Clone(f.Method)
}
