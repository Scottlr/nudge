package codex

import (
	"bytes"
	"encoding/json"
	"errors"
)

var (
	ErrFrameTooLarge = errors.New("app-server frame exceeds limit")
	ErrEmptyFrame    = errors.New("app-server frame is empty")
	ErrPartialFrame  = errors.New("app-server stream ended with a partial frame")
	ErrFramerClosed  = errors.New("app-server framer is closed")
)

// Framer incrementally splits stdio JSONL without retaining more than one
// configured incomplete frame. Newline bytes are transport delimiters and are
// not part of the returned JSON payload.
type Framer struct {
	maxBytes int
	pending  []byte
	closed   bool
}

// NewFramer constructs a bounded JSONL framer.
func NewFramer(maxBytes int) (*Framer, error) {
	if maxBytes <= 0 {
		return nil, ErrFrameTooLarge
	}
	return &Framer{maxBytes: maxBytes}, nil
}

// Feed consumes arbitrary stdout chunks and returns every complete frame
// found in the chunk. It never waits for a consumer.
func (f *Framer) Feed(chunk []byte) ([][]byte, error) {
	if f == nil || f.closed {
		return nil, ErrFramerClosed
	}
	var frames [][]byte
	for len(chunk) > 0 {
		newline := bytes.IndexByte(chunk, '\n')
		if newline < 0 {
			if len(f.pending)+len(chunk) > f.maxBytes {
				return nil, ErrFrameTooLarge
			}
			f.pending = append(f.pending, chunk...)
			break
		}
		line := chunk[:newline]
		if len(f.pending)+len(line) > f.maxBytes {
			return nil, ErrFrameTooLarge
		}
		frame := make([]byte, len(f.pending)+len(line))
		copy(frame, f.pending)
		copy(frame[len(f.pending):], line)
		f.pending = f.pending[:0]
		if len(frame) > 0 && frame[len(frame)-1] == '\r' {
			frame = frame[:len(frame)-1]
		}
		if len(bytes.TrimSpace(frame)) == 0 {
			return nil, ErrEmptyFrame
		}
		frames = append(frames, frame)
		chunk = chunk[newline+1:]
	}
	return frames, nil
}

// Finish marks the stream closed. App-server frames must be newline
// terminated; an unterminated final payload is treated as partial truth.
func (f *Framer) Finish() error {
	if f == nil || f.closed {
		return ErrFramerClosed
	}
	f.closed = true
	if len(f.pending) != 0 {
		return ErrPartialFrame
	}
	return nil
}

// EncodeLine validates a complete JSON payload and appends exactly one JSONL
// newline without exceeding maxBytes for the payload itself.
func EncodeLine(payload []byte, maxBytes int) ([]byte, error) {
	if len(payload) == 0 || len(payload) > maxBytes || !json.Valid(payload) {
		return nil, ErrFrameTooLarge
	}
	line := make([]byte, len(payload)+1)
	copy(line, payload)
	line[len(payload)] = '\n'
	return line, nil
}
