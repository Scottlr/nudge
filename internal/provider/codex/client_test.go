package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/process"
	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

const (
	helperEnv  = "NUDGE_CODEX_HELPER"
	helperMode = "NUDGE_CODEX_HELPER_MODE"
)

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		runFakeAppServer(os.Getenv(helperMode))
		return
	}
	os.Exit(m.Run())
}

func TestClientRoutesResponses(t *testing.T) {
	client := newTestClient(t, "out_of_order", Config{})
	defer client.Close()

	var first, second struct {
		Value string `json:"value"`
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, target := range []*struct {
		Value string `json:"value"`
	}{&first, &second} {
		wg.Add(1)
		go func(result *struct {
			Value string `json:"value"`
		}) {
			defer wg.Done()
			results <- client.Call(context.Background(), "test/request", nil, result)
		}(target)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
	}
	if (first.Value != "first" && first.Value != "second") || first.Value == second.Value {
		t.Fatalf("responses were not independently routed: first=%q second=%q", first.Value, second.Value)
	}
}

func TestClientHandlesServerRequest(t *testing.T) {
	client := newTestClient(t, "server_request", Config{})
	defer client.Close()
	if err := client.RegisterServerRequestHandler("server/request", func(_ context.Context, request protocol.ServerRequest) (json.RawMessage, error) {
		if request.ID.String() != "99" {
			return nil, fmt.Errorf("unexpected request ID %s", request.ID.String())
		}
		return json.RawMessage(`{"accepted":true}`), nil
	}); err != nil {
		t.Fatalf("RegisterServerRequestHandler() error = %v", err)
	}

	var result struct {
		Completed bool `json:"completed"`
	}
	if err := client.Call(context.Background(), "test/request", nil, &result); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !result.Completed {
		t.Fatal("server did not observe the request response")
	}
}

func TestClientRejectsOversizedFrame(t *testing.T) {
	config := Config{MaxFrameBytes: 64, QueueEvents: 4, QueueResidentBytes: 256, MaxPendingCalls: 4, MaxStderrBytes: 1024}
	client := newTestClient(t, "oversized", config)
	defer client.Close()

	err := client.Call(context.Background(), "test/request", nil, nil)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("Call() error = %v, want ErrFrameTooLarge", err)
	}
}

func TestMalformedFrameFailsPendingCall(t *testing.T) {
	client := newTestClient(t, "malformed", Config{})
	defer client.Close()

	err := client.Call(context.Background(), "test/request", nil, nil)
	if !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("Call() error = %v, want ErrMalformedFrame", err)
	}
}

func TestUnknownNotificationIsSafe(t *testing.T) {
	unknown := make(chan string, 1)
	config := DefaultConfig()
	config.UnknownNotification = func(method string) { unknown <- method }
	client := newTestClient(t, "unknown_notification", config)
	defer client.Close()

	var result struct {
		OK bool `json:"ok"`
	}
	if err := client.Call(context.Background(), "test/request", nil, &result); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !result.OK {
		t.Fatal("response was not decoded")
	}
	select {
	case method := <-unknown:
		if method != "future/notification" {
			t.Fatalf("unknown notification method = %q", method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unknown notification was not dispatched")
	}
}

func TestFramerRejectsPartialFrame(t *testing.T) {
	framer, err := NewFramer(64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := framer.Feed([]byte(`{"id":1`)); err != nil {
		t.Fatal(err)
	}
	if err := framer.Finish(); !errors.Is(err, ErrPartialFrame) {
		t.Fatalf("Finish() error = %v, want ErrPartialFrame", err)
	}
}

func TestFrameQueueRejectsSaturation(t *testing.T) {
	queue := newFrameQueue(1, 128)
	frame := protocol.Frame{Kind: protocol.FrameNotification, Method: "future/notification"}
	if err := queue.push(frame, 64); err != nil {
		t.Fatal(err)
	}
	if err := queue.push(frame, 64); !errors.Is(err, ErrProviderEventOverflow) {
		t.Fatalf("second push error = %v, want ErrProviderEventOverflow", err)
	}
}

func newTestClient(t *testing.T, mode string, config Config) *Client {
	t.Helper()
	resolver := process.NewExecutableResolver()
	executablePath, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	executable, err := resolver.Resolve(context.Background(), process.ResolveExecutableRequest{
		Kind:           process.ExecutableCodex,
		ConfiguredPath: executablePath,
		CurrentDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if config.Environment.Set == nil {
		config.Environment.Set = make(map[string]string)
	}
	config.Environment.Mode = process.EnvironmentMinimal
	config.Environment.Set[helperEnv] = "1"
	config.Environment.Set[helperMode] = mode
	client, err := NewClient(context.Background(), process.NewRunner(), executable, config)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func runFakeAppServer(mode string) {
	input := bufio.NewScanner(os.Stdin)
	input.Buffer(make([]byte, 1024), 1024*1024)
	output := bufio.NewWriter(os.Stdout)
	defer output.Flush()

	readRequest := func() protocol.Frame {
		if !input.Scan() {
			os.Exit(2)
		}
		frame, err := protocol.ParseFrame(input.Bytes())
		if err != nil {
			os.Exit(3)
		}
		return frame
	}
	write := func(value string) {
		_, _ = output.WriteString(value)
		_, _ = output.WriteString("\n")
		_ = output.Flush()
	}

	switch mode {
	case "lifecycle":
		initialize := readRequest()
		if initialize.Method != "initialize" || strings.Contains(string(initialize.Params), `"capabilities"`) {
			os.Exit(6)
		}
		write(responseLine(initialize.ID, `{"codexHome":"C:\\codex","platformFamily":"windows","platformOs":"windows","userAgent":"codex-cli 0.144.0-alpha.4"}`))
		initialized := readRequest()
		if initialized.Kind != protocol.FrameNotification || initialized.Method != "initialized" {
			os.Exit(7)
		}
		account := readRequest()
		if account.Method != "account/read" {
			os.Exit(8)
		}
		write(responseLine(account.ID, `{"account":{"type":"chatgpt","planType":"plus","email":"secret@example.com"},"requiresOpenaiAuth":false}`))
		for input.Scan() {
		}
	case "unsupported_lifecycle":
		initialize := readRequest()
		if initialize.Method != "initialize" {
			os.Exit(9)
		}
		write(responseLine(initialize.ID, `{"codexHome":"C:\\codex","platformFamily":"windows","platformOs":"windows","userAgent":"codex-cli 0.143.0-alpha.4"}`))
	case "login":
		initialize := readRequest()
		if initialize.Method != "initialize" {
			os.Exit(10)
		}
		write(responseLine(initialize.ID, `{"codexHome":"C:\\codex","platformFamily":"windows","platformOs":"windows","userAgent":"codex-cli 0.144.0-alpha.4"}`))
		initialized := readRequest()
		if initialized.Kind != protocol.FrameNotification || initialized.Method != "initialized" {
			os.Exit(11)
		}
		account := readRequest()
		if account.Method != "account/read" {
			os.Exit(12)
		}
		write(responseLine(account.ID, `{"account":null,"requiresOpenaiAuth":true}`))
		login := readRequest()
		if login.Method != "account/login/start" || !strings.Contains(string(login.Params), `"type":"chatgpt"`) {
			os.Exit(13)
		}
		write(responseLine(login.ID, `{"type":"chatgpt","loginId":"login-1","authUrl":"https://auth.example.test/login"}`))
		for input.Scan() {
		}
	case "conversation_lifecycle":
		start := readRequest()
		if start.Method != "thread/start" || !strings.Contains(string(start.Params), `"sandbox":"read-only"`) {
			os.Exit(14)
		}
		write(responseLine(start.ID, `{"thread":{"id":"codex-thread-1"}}`))
		resume := readRequest()
		if resume.Method != "thread/resume" || !strings.Contains(string(resume.Params), `"threadId":"codex-thread-1"`) {
			os.Exit(15)
		}
		write(responseLine(resume.ID, `{"thread":{"id":"codex-thread-1"}}`))
		turnStart := readRequest()
		if turnStart.Method != "turn/start" || !strings.Contains(string(turnStart.Params), `"text":"hello"`) {
			os.Exit(16)
		}
		write(responseLine(turnStart.ID, `{"turn":{"id":"codex-turn-1"}}`))
		steer := readRequest()
		if steer.Method != "turn/steer" || !strings.Contains(string(steer.Params), `"expectedTurnId":"codex-turn-1"`) || !strings.Contains(string(steer.Params), `"text":"continue"`) {
			os.Exit(17)
		}
		write(responseLine(steer.ID, `{"turnId":"codex-turn-1"}`))
		interrupt := readRequest()
		if interrupt.Method != "turn/interrupt" || !strings.Contains(string(interrupt.Params), `"turnId":"codex-turn-1"`) {
			os.Exit(18)
		}
		write(responseLine(interrupt.ID, `{}`))
	case "out_of_order":
		first := readRequest()
		second := readRequest()
		write(responseLine(second.ID, `{"value":"second"}`))
		write(responseLine(first.ID, `{"value":"first"}`))
	case "server_request":
		request := readRequest()
		write(`{"id":99,"method":"server/request","params":{"request":true}}`)
		response := readRequest()
		if response.Kind != protocol.FrameResponse || response.Error != nil || string(response.Result) != `{"accepted":true}` {
			os.Exit(4)
		}
		write(responseLine(request.ID, `{"completed":true}`))
	case "oversized":
		_ = readRequest()
		write("{" + strings.Repeat("x", 128) + "}")
	case "malformed":
		_ = readRequest()
		write("{not-json")
	case "unknown_notification":
		request := readRequest()
		write(`{"method":"future/notification","params":{"newField":true}}`)
		write(responseLine(request.ID, `{"ok":true}`))
	default:
		os.Exit(5)
	}
}

func responseLine(id protocol.RequestID, result string) string {
	encoded, _ := json.Marshal(id)
	return fmt.Sprintf(`{"id":%s,"result":%s}`, encoded, result)
}
