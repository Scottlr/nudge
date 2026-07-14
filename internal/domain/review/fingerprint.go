package review

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

// AnchorFingerprintVersion identifies the normalization and hashing contract
// used by new anchors. It is part of the stored evidence contract.
const AnchorFingerprintVersion uint32 = 1

// NormalizeAnchorFingerprintText changes only line endings and trailing
// whitespace. Indentation and interior whitespace remain evidence.
func NormalizeAnchorFingerprintText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	return strings.Join(lines, "\n")
}

// FingerprintSelection returns the stable content identity of one selected
// range. File path, side, and line coordinates are deliberately excluded so
// this evidence can survive a conservative relocation.
func FingerprintSelection(value string) string {
	h := sha256.New()
	writeFingerprintPart(h, "nudge-anchor-selection-v1")
	writeFingerprintPart(h, NormalizeAnchorFingerprintText(value))
	return hex.EncodeToString(h.Sum(nil))
}

// FingerprintContext returns the stable identity of ordered context lines.
// An empty context is represented by an empty hash, matching the anchor
// builder's edge-of-hunk behavior.
func FingerprintContext(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	h := sha256.New()
	writeFingerprintPart(h, "nudge-anchor-context-v1")
	for _, line := range lines {
		writeFingerprintPart(h, NormalizeAnchorFingerprintText(line))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// LegacyFingerprintSelection reproduces the pre-reconciliation anchor hash
// so local pre-release anchors remain searchable after the v1 contract is
// introduced.
func LegacyFingerprintSelection(side repository.DiffSide, path repository.RepoPath, start, end int, value string) string {
	h := sha256.New()
	writeFingerprintPart(h, "nudge-anchor-selection-v1")
	writeFingerprintPart(h, string(side))
	writeFingerprintPart(h, string(path))
	writeFingerprintPart(h, strconv.Itoa(start))
	writeFingerprintPart(h, strconv.Itoa(end))
	writeFingerprintPart(h, NormalizeAnchorFingerprintText(value))
	return hex.EncodeToString(h.Sum(nil))
}

// LegacyContextLine is one line of the pre-reconciliation context evidence.
type LegacyContextLine struct {
	Line int
	Text string
}

// LegacyFingerprintContext reproduces the old line-number-bound context hash
// for anchors created before content-only fingerprints were introduced.
func LegacyFingerprintContext(lines []LegacyContextLine) string {
	if len(lines) == 0 {
		return ""
	}
	var payload strings.Builder
	for index, line := range lines {
		part := strconv.Itoa(line.Line) + ":" + NormalizeAnchorFingerprintText(line.Text)
		if index > 0 {
			payload.WriteByte('\n')
		}
		payload.WriteString(part)
	}
	h := sha256.New()
	writeFingerprintPart(h, "nudge-anchor-context-v1")
	writeFingerprintPart(h, payload.String())
	return hex.EncodeToString(h.Sum(nil))
}

func writeFingerprintPart(h hash.Hash, value string) {
	_, _ = h.Write([]byte(strconv.Itoa(len(value))))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(value))
	_, _ = h.Write([]byte{0})
}

func validFingerprintText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
