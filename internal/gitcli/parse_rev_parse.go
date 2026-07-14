package gitcli

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func parseOutputLine(name string, data []byte, allowEmpty bool) (string, error) {
	value := string(data)
	value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	if strings.ContainsAny(value, "\r\n") || !utf8.ValidString(value) || (!allowEmpty && value == "") {
		return "", malformed("invalid %s output", name)
	}
	for _, r := range value {
		if r == 0 || unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return "", malformed("unsafe %s output", name)
		}
	}
	return value, nil
}

func parseOutputLines(name string, data []byte) ([]string, error) {
	value := string(data)
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		line, err := parseOutputLine(name, []byte(part), true)
		if err != nil {
			return nil, err
		}
		if line == "" {
			return nil, malformed("empty %s output line", name)
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func parseBooleanOutput(name string, data []byte) (bool, error) {
	value, err := parseOutputLine(name, data, false)
	if err != nil {
		return false, err
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, malformed("invalid %s boolean", name)
	}
}
