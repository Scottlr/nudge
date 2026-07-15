package protocol

import (
	"encoding/json"
	"testing"
)

func TestParseFramePreservesStringRequestID(t *testing.T) {
	frame, err := ParseFrame([]byte(`{"id":"server-1","method":"future/request","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Kind != FrameServerRequest || frame.ID.String() != "server-1" || !frame.ID.IsString() {
		t.Fatalf("unexpected frame: kind=%v id=%q string=%v", frame.Kind, frame.ID.String(), frame.ID.IsString())
	}
	payload, err := MarshalResponse(frame.ID, json.RawMessage(`{"ok":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"id":"server-1","result":{"ok":true}}` {
		t.Fatalf("response payload = %s", payload)
	}
}

func TestParseFrameRejectsNullRequestID(t *testing.T) {
	if _, err := ParseFrame([]byte(`{"id":null,"result":{}}`)); err == nil {
		t.Fatal("ParseFrame() accepted null request ID")
	}
}
