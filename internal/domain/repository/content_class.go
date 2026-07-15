package repository

import (
	"bytes"
	"errors"
	"unicode/utf8"
)

// ContentClassV1 is the one persisted classification of regular-file bytes.
// It is deliberately independent from presentation or Git's display hint.
type ContentClassV1 string

const (
	ContentClassRegularTextUTF8 ContentClassV1 = "regular_text_utf8"
	ContentClassRegularBinary   ContentClassV1 = "regular_binary"
	ContentClassOpaqueBytes     ContentClassV1 = "opaque_bytes"
)

// Validate checks the closed v1 content-class vocabulary.
func (c ContentClassV1) Validate() error {
	switch c {
	case ContentClassRegularTextUTF8, ContentClassRegularBinary, ContentClassOpaqueBytes:
		return nil
	default:
		return ErrInvalidContentClass
	}
}

// IsByteOriented reports whether text-line semantics must be disabled.
func (c ContentClassV1) IsByteOriented() bool {
	return c == ContentClassRegularBinary || c == ContentClassOpaqueBytes
}

// ClassifyContentV1 applies the deterministic v1 rule to one complete byte
// value. Explicit Git binary-patch evidence wins over encoding inspection.
func ClassifyContentV1(data []byte, explicitBinaryPatch bool) ContentClassV1 {
	if explicitBinaryPatch || bytes.IndexByte(data, 0) >= 0 {
		return ContentClassRegularBinary
	}
	if utf8.Valid(data) {
		return ContentClassRegularTextUTF8
	}
	return ContentClassOpaqueBytes
}

// ContentClassifierV1 incrementally applies ClassifyContentV1 without
// retaining the streamed body. It is safe for arbitrary chunk boundaries.
type ContentClassifierV1 struct {
	explicitBinaryPatch bool
	nul                 bool
	invalid             bool
	pending             []byte
}

// NewContentClassifierV1 creates a bounded streaming classifier.
func NewContentClassifierV1(explicitBinaryPatch bool) *ContentClassifierV1 {
	return &ContentClassifierV1{explicitBinaryPatch: explicitBinaryPatch}
}

// Write observes bytes without retaining them and implements io.Writer.
func (c *ContentClassifierV1) Write(data []byte) (int, error) {
	if c == nil {
		return 0, ErrInvalidContentClass
	}
	if bytes.IndexByte(data, 0) >= 0 {
		c.nul = true
	}
	if c.invalid {
		return len(data), nil
	}
	combined := make([]byte, 0, len(c.pending)+len(data))
	combined = append(combined, c.pending...)
	combined = append(combined, data...)
	c.pending = c.pending[:0]
	for len(combined) > 0 {
		_, size := utf8.DecodeRune(combined)
		if size == 1 && !utf8.FullRune(combined) {
			c.pending = append(c.pending, combined...)
			break
		}
		if size == 1 && combined[0] >= utf8.RuneSelf {
			c.invalid = true
			c.pending = c.pending[:0]
			break
		}
		combined = combined[size:]
	}
	return len(data), nil
}

// Classify finishes the stream and returns its persisted v1 class.
func (c *ContentClassifierV1) Classify() ContentClassV1 {
	if c == nil {
		return ""
	}
	if c.explicitBinaryPatch || c.nul {
		return ContentClassRegularBinary
	}
	if c.invalid || len(c.pending) != 0 {
		return ContentClassOpaqueBytes
	}
	return ContentClassRegularTextUTF8
}

var ErrInvalidContentClass = errors.New("invalid content class")
