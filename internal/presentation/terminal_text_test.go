package presentation

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestProjectTerminalTextScalarNeutralizesHostileInput(t *testing.T) {
	input := string([]byte{0xff}) + "A\x1b[31mx\t\n" + "\u009b31m" + "\u202Ename"

	got := ProjectTerminalText(input, TerminalTextScalar)
	want := "�A�[31mx\\t\\n�31m�name"
	if got != want {
		t.Fatalf("projection = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Fatal("projection is not valid UTF-8")
	}
	if strings.ContainsAny(got, "\x1b\x7f") {
		t.Fatalf("projection retained terminal controls: %q", got)
	}
}

func TestProjectTerminalTextMultilinePreservesNormalizedBoundaries(t *testing.T) {
	input := "first\r\nsecond\rthird\nfourth\t\u200F"
	got := ProjectTerminalText(input, TerminalTextMultiline)
	want := "first\nsecond\nthird\nfourth\\t�"
	if got != want {
		t.Fatalf("projection = %q, want %q", got, want)
	}
	if strings.Contains(got, "\r") {
		t.Fatal("projection retained carriage returns")
	}
	if input != "first\r\nsecond\rthird\nfourth\t\u200F" {
		t.Fatal("canonical input was changed")
	}
}
