package repository

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"unicode/utf8"
)

var (
	// ErrInvalidTextByteSemantics reports contradictory exact-text evidence.
	ErrInvalidTextByteSemantics = errors.New("invalid text byte semantics")
	// ErrTextContentClassDrift reports text evidence that disagrees with the
	// single persisted ContentClassV1 decision.
	ErrTextContentClassDrift = errors.New("text content class evidence drift")
	// ErrInvalidTextLineProjection reports a malformed raw/display line pair.
	ErrInvalidTextLineProjection = errors.New("invalid text line projection")
)

// TextEncodingClass is the lossless encoding decision for regular text.
type TextEncodingClass string

const (
	TextUTF8            TextEncodingClass = "utf8"
	TextUTF8BOM         TextEncodingClass = "utf8_bom"
	TextEncodingUnknown TextEncodingClass = "unsupported_or_invalid"
)

func (c TextEncodingClass) Validate() error {
	switch c {
	case TextUTF8, TextUTF8BOM, TextEncodingUnknown:
		return nil
	default:
		return ErrInvalidTextByteSemantics
	}
}

// LineTerminator identifies the exact terminator represented by one logical
// line. TerminatorNone is used for the final unterminated line.
type LineTerminator string

const (
	TerminatorNone LineTerminator = "none"
	TerminatorLF   LineTerminator = "lf"
	TerminatorCRLF LineTerminator = "crlf"
	TerminatorCR   LineTerminator = "cr"
)

func (t LineTerminator) Validate() error {
	switch t {
	case TerminatorNone, TerminatorLF, TerminatorCRLF, TerminatorCR:
		return nil
	default:
		return ErrInvalidTextLineProjection
	}
}

// LineEndingProfile is a byte-level count. CRLF is counted once as CRLF and
// is never also counted as a bare CR or LF.
type LineEndingProfile struct {
	LFCount   uint64
	CRLFCount uint64
	CRCount   uint64
	FinalLF   bool
	Mixed     bool
}

func (p LineEndingProfile) Validate() error {
	if p.FinalLF && p.LFCount+p.CRLFCount == 0 {
		return ErrInvalidTextByteSemantics
	}
	if p.Mixed && nonZeroCount(p.LFCount)+nonZeroCount(p.CRLFCount)+nonZeroCount(p.CRCount) < 2 {
		return ErrInvalidTextByteSemantics
	}
	return nil
}

func nonZeroCount(value uint64) uint64 {
	if value == 0 {
		return 0
	}
	return 1
}

// TextByteSemantics is immutable identity and line-ending evidence for one
// exact regular-text byte stream. Display strings are derived and are not
// part of this value.
type TextByteSemantics struct {
	Encoding   TextEncodingClass
	ByteLength uint64
	SHA256     string
	HasBOM     bool
	Endings    LineEndingProfile
	Empty      bool
}

func (s TextByteSemantics) Validate() error {
	if s.Encoding.Validate() != nil || !validSHA256(s.SHA256) || s.Empty != (s.ByteLength == 0) || s.HasBOM != (s.Encoding == TextUTF8BOM) || s.Endings.Validate() != nil {
		return ErrInvalidTextByteSemantics
	}
	if s.Encoding == TextEncodingUnknown && s.HasBOM {
		return ErrInvalidTextByteSemantics
	}
	return nil
}

// ClassifyTextBytes classifies a bounded exact byte slice without stripping a
// BOM or normalizing any line ending.
func ClassifyTextBytes(data []byte) (TextByteSemantics, error) {
	return classifyTextReader(bytes.NewReader(data), uint64(len(data)))
}

// ClassifyTextReader classifies an exact stream and requires expectedLength
// bytes. It retains only a bounded scanner buffer and hashes every byte.
func ClassifyTextReader(reader io.Reader, expectedLength uint64) (TextByteSemantics, error) {
	if reader == nil {
		return TextByteSemantics{}, ErrInvalidTextByteSemantics
	}
	return classifyTextReader(reader, expectedLength)
}

// ClassifyPersistedText verifies that T057 already classified the content as
// regular UTF-8 text before producing the richer T087 evidence.
func ClassifyPersistedText(reader io.Reader, expectedLength uint64, contentClass ContentClassV1) (TextByteSemantics, error) {
	if contentClass != ContentClassRegularTextUTF8 {
		return TextByteSemantics{}, ErrTextContentClassDrift
	}
	result, err := ClassifyTextReader(reader, expectedLength)
	if err != nil {
		return TextByteSemantics{}, err
	}
	if result.Encoding == TextEncodingUnknown {
		return TextByteSemantics{}, ErrTextContentClassDrift
	}
	return result, nil
}

func classifyTextReader(reader io.Reader, expectedLength uint64) (TextByteSemantics, error) {
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	var total uint64
	var previousCR bool
	var profile LineEndingProfile
	var prefix [3]byte
	prefixLength := 0
	valid := true
	var pendingUTF8 []byte
	var lastByte byte
	for {
		read, err := reader.Read(buffer)
		if read > 0 {
			chunk := buffer[:read]
			if total > ^uint64(0)-uint64(read) {
				return TextByteSemantics{}, ErrInvalidTextByteSemantics
			}
			total += uint64(read)
			if _, writeErr := hash.Write(chunk); writeErr != nil {
				return TextByteSemantics{}, writeErr
			}
			if prefixLength < len(prefix) {
				copyCount := len(prefix) - prefixLength
				if copyCount > len(chunk) {
					copyCount = len(chunk)
				}
				copy(prefix[prefixLength:], chunk[:copyCount])
				prefixLength += copyCount
			}
			lastByte = chunk[len(chunk)-1]
			if valid {
				combined := append(append([]byte(nil), pendingUTF8...), chunk...)
				pendingUTF8 = pendingUTF8[:0]
				for index := 0; index < len(combined); {
					value, size := utf8.DecodeRune(combined[index:])
					if value == utf8.RuneError && size == 1 {
						if !utf8.FullRune(combined[index:]) {
							pendingUTF8 = append(pendingUTF8, combined[index:]...)
							break
						}
						valid = false
						break
					}
					index += size
				}
			}
			for _, value := range chunk {
				switch value {
				case '\r':
					previousCR = true
				case '\n':
					if previousCR {
						profile.CRLFCount++
					} else {
						profile.LFCount++
					}
					previousCR = false
				default:
					if previousCR {
						profile.CRCount++
						previousCR = false
					}
				}
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return TextByteSemantics{}, err
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return TextByteSemantics{}, err
			}
			break
		}
		if read == 0 {
			return TextByteSemantics{}, io.ErrNoProgress
		}
	}
	if previousCR {
		profile.CRCount++
	}
	if len(pendingUTF8) != 0 {
		valid = false
	}
	if total != expectedLength {
		return TextByteSemantics{}, ErrTextContentClassDrift
	}
	profile.FinalLF = total > 0 && lastByte == '\n'
	profile.Mixed = nonZeroCount(profile.LFCount)+nonZeroCount(profile.CRLFCount)+nonZeroCount(profile.CRCount) > 1
	encoding := TextUTF8
	hasBOM := valid && prefixLength >= 3 && bytes.Equal(prefix[:], []byte{0xef, 0xbb, 0xbf})
	if hasBOM {
		encoding = TextUTF8BOM
	}
	// The stream classifier must validate the full byte sequence. A bounded
	// second pass is used by callers with seekable data; the slice helper below
	// provides the authoritative validation for normal content.
	if expectedLength == 0 {
		encoding = TextUTF8
	} else if !valid {
		encoding = TextEncodingUnknown
	}
	result := TextByteSemantics{Encoding: encoding, ByteLength: total, SHA256: hex.EncodeToString(hash.Sum(nil)), HasBOM: hasBOM, Endings: profile, Empty: total == 0}
	if err := result.Validate(); err != nil {
		return TextByteSemantics{}, err
	}
	return result, nil
}

// TextByteSemanticsWriter observes an exact regular-text stream without
// retaining its body. It is used while materializing immutable manifests.
type TextByteSemanticsWriter struct {
	hash interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
	total        uint64
	previousCR   bool
	lastByte     byte
	profile      LineEndingProfile
	prefix       [3]byte
	prefixLength int
	valid        bool
	pending      []byte
}

// NewTextByteSemanticsWriter creates a streaming exact-text observer.
func NewTextByteSemanticsWriter() *TextByteSemanticsWriter {
	return &TextByteSemanticsWriter{hash: sha256.New(), valid: true}
}

// Write implements io.Writer and records every byte in the identity.
func (w *TextByteSemanticsWriter) Write(data []byte) (int, error) {
	if w == nil {
		return 0, ErrInvalidTextByteSemantics
	}
	if len(data) == 0 {
		return 0, nil
	}
	if w.total > ^uint64(0)-uint64(len(data)) {
		return 0, ErrInvalidTextByteSemantics
	}
	w.total += uint64(len(data))
	if _, err := w.hash.Write(data); err != nil {
		return 0, err
	}
	w.lastByte = data[len(data)-1]
	if w.prefixLength < len(w.prefix) {
		copyCount := len(w.prefix) - w.prefixLength
		if copyCount > len(data) {
			copyCount = len(data)
		}
		copy(w.prefix[w.prefixLength:], data[:copyCount])
		w.prefixLength += copyCount
	}
	for _, value := range data {
		switch value {
		case '\r':
			if w.previousCR {
				w.profile.CRCount++
			}
			w.previousCR = true
		case '\n':
			if w.previousCR {
				w.profile.CRLFCount++
			} else {
				w.profile.LFCount++
			}
			w.previousCR = false
		default:
			if w.previousCR {
				w.profile.CRCount++
				w.previousCR = false
			}
		}
	}
	if w.valid {
		combined := append(append([]byte(nil), w.pending...), data...)
		w.pending = w.pending[:0]
		for index := 0; index < len(combined); {
			value, size := utf8.DecodeRune(combined[index:])
			if value == utf8.RuneError && size == 1 {
				if !utf8.FullRune(combined[index:]) {
					w.pending = append(w.pending, combined[index:]...)
					break
				}
				w.valid = false
				break
			}
			index += size
		}
	}
	return len(data), nil
}

// Semantics finalizes the observer and requires the expected exact length.
func (w *TextByteSemanticsWriter) Semantics(expectedLength uint64) (TextByteSemantics, error) {
	if w == nil || w.total != expectedLength {
		return TextByteSemantics{}, ErrTextContentClassDrift
	}
	profile := w.profile
	if w.previousCR {
		profile.CRCount++
	}
	profile.FinalLF = w.total > 0 && w.lastByte == '\n'
	profile.Mixed = nonZeroCount(profile.LFCount)+nonZeroCount(profile.CRLFCount)+nonZeroCount(profile.CRCount) > 1
	if len(w.pending) != 0 {
		w.valid = false
	}
	encoding := TextUTF8
	hasBOM := w.valid && w.prefixLength >= 3 && bytes.Equal(w.prefix[:], []byte{0xef, 0xbb, 0xbf})
	if !w.valid {
		encoding = TextEncodingUnknown
	} else if hasBOM {
		encoding = TextUTF8BOM
	}
	result := TextByteSemantics{Encoding: encoding, ByteLength: w.total, SHA256: hex.EncodeToString(w.hash.Sum(nil)), HasBOM: hasBOM, Endings: profile, Empty: w.total == 0}
	if err := result.Validate(); err != nil {
		return TextByteSemantics{}, err
	}
	return result, nil
}

// ProjectTextLines produces bounded, lossless line metadata. Raw ranges
// include the terminator; DisplayText omits CR from CRLF and never replaces
// source bytes.
func ProjectTextLines(data []byte) ([]TextLineProjection, error) {
	if !utf8.Valid(data) {
		return nil, ErrTextContentClassDrift
	}
	lines := make([]TextLineProjection, 0, 1)
	start := 0
	for start < len(data) {
		end := start
		for end < len(data) && data[end] != '\n' && data[end] != '\r' {
			end++
		}
		terminator := TerminatorNone
		contentEnd := end
		if end < len(data) {
			if data[end] == '\r' && end+1 < len(data) && data[end+1] == '\n' {
				terminator = TerminatorCRLF
				end += 2
			} else if data[end] == '\r' {
				terminator = TerminatorCR
				end++
			} else {
				terminator = TerminatorLF
				end++
			}
		}
		lines = append(lines, TextLineProjection{RawStart: int64(start), RawEnd: int64(end), Terminator: terminator, DisplayText: string(data[start:contentEnd])})
		start = end
	}
	if len(data) == 0 || (len(data) > 0 && (data[len(data)-1] == '\n' || data[len(data)-1] == '\r')) {
		if len(data) == 0 {
			lines = append(lines, TextLineProjection{RawStart: 0, RawEnd: 0, Terminator: TerminatorNone})
		}
	}
	return lines, nil
}

// TextLineProjection is a display-only line derived from exact raw bytes.
type TextLineProjection struct {
	RawStart    int64
	RawEnd      int64
	Terminator  LineTerminator
	DisplayText string
}

func (p TextLineProjection) Validate() error {
	if p.RawStart < 0 || p.RawEnd < p.RawStart || p.Terminator.Validate() != nil || !utf8.ValidString(p.DisplayText) {
		return ErrInvalidTextLineProjection
	}
	return nil
}

// ScanTextLines applies a bounded line callback without retaining the whole
// source. It is useful for immutable range readers that already enforce a
// byte ceiling.
func ScanTextLines(reader io.Reader, callback func(TextLineProjection) error) error {
	if reader == nil || callback == nil {
		return ErrInvalidTextLineProjection
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for scanner.Scan() {
		value := scanner.Bytes()
		terminator := TerminatorLF
		if len(value) > 0 && value[len(value)-1] == '\r' {
			value = value[:len(value)-1]
			terminator = TerminatorCRLF
		}
		if err := callback(TextLineProjection{Terminator: terminator, DisplayText: string(value)}); err != nil {
			return err
		}
	}
	return scanner.Err()
}
