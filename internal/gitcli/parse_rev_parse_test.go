package gitcli

import "testing"

func TestParseOutputLinePreservesSpacesAndRejectsAdditionalLines(t *testing.T) {
	value, err := parseOutputLine("path", []byte("C:/repo/with spaces\n"), false)
	if err != nil || value != "C:/repo/with spaces" {
		t.Fatalf("parsed path = %q, error = %v", value, err)
	}
	if _, err := parseOutputLine("path", []byte("first\nsecond\n"), false); err == nil {
		t.Fatal("multi-line machine output was accepted")
	}
	if _, err := parseBooleanOutput("boolean", []byte("maybe\n")); err == nil {
		t.Fatal("invalid boolean machine output was accepted")
	}
}
