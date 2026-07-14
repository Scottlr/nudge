package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("NUDGE_PROCESS_HELPER") == "1" {
		runProcessHelper()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runProcessHelper() {
	args := helperArgs()
	switch os.Getenv("NUDGE_HELPER_MODE") {
	case "args":
		_ = json.NewEncoder(os.Stdout).Encode(args)
	case "verbose":
		chunk := make([]byte, 32*1024)
		for i := 0; i < 256; i++ {
			if _, err := os.Stdout.Write(chunk); err != nil {
				return
			}
		}
	case "stream":
		if len(args) > 0 {
			_, _ = io.WriteString(os.Stdout, args[0])
			_, _ = io.WriteString(os.Stderr, args[0])
		}
	case "env":
		values := make(map[string]string, len(args))
		for _, key := range args {
			values[key] = os.Getenv(key)
		}
		_ = json.NewEncoder(os.Stdout).Encode(values)
	case "managed":
		_, _ = io.WriteString(os.Stdout, "ready\n")
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "sleep":
		<-time.After(time.Hour)
	case "stdin":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	}
}

func helperArgs() []string {
	for i, arg := range os.Args {
		if arg == "--" {
			return append([]string(nil), os.Args[i+1:]...)
		}
	}
	return nil
}

func helperSpec(mode string, args ...string) Spec {
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		executable = os.Args[0]
	}
	currentDir, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("get current directory: %v", err))
	}
	identity, err := NewExecutableResolver().Resolve(context.Background(), ResolveExecutableRequest{
		Kind:           ExecutableGit,
		ConfiguredPath: executable,
		CurrentDir:     currentDir,
	})
	if err != nil {
		panic(fmt.Sprintf("resolve helper executable %q from %q: %v", executable, currentDir, err))
	}
	return Spec{
		Executable: identity,
		Args:       append([]string{"-test.run=TestProcessHelper", "--"}, args...),
		Environment: EnvironmentPolicy{
			Mode: EnvironmentMinimal,
			Set: map[string]string{
				"NUDGE_PROCESS_HELPER": "1",
				"NUDGE_HELPER_MODE":    mode,
			},
		},
		StdoutLimit: 1 << 20,
		StderrLimit: 1 << 20,
	}
}

func TestRunnerPassesArgumentsLiterally(t *testing.T) {
	want := []string{"two words", "世界", "-leading-dash", "$(not-a-shell) & |"}
	runner := NewRunner()
	result, err := runner.Run(context.Background(), helperSpec("args", want...))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var got []string
	if err := json.Unmarshal(result.Stdout, &got); err != nil {
		t.Fatalf("decode helper arguments: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argument %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRunnerBoundsAndDrainsOutput(t *testing.T) {
	spec := helperSpec("verbose")
	spec.StdoutLimit = 64 * 1024
	spec.StderrLimit = 64 * 1024
	result, err := NewRunner().Run(context.Background(), spec)
	var limitErr *LimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("Run() error = %v, want LimitError", err)
	}
	if limitErr.Stream != StreamStdout {
		t.Fatalf("limit stream = %q, want stdout", limitErr.Stream)
	}
	if !result.StdoutTruncated || int64(len(result.Stdout)) != spec.StdoutLimit {
		t.Fatalf("stdout length/truncation = %d/%t", len(result.Stdout), result.StdoutTruncated)
	}
}

func TestRunnerCancelsProcess(t *testing.T) {
	sink := &recordingSink{ready: make(chan struct{})}
	proc, err := NewRunner().Start(context.Background(), helperSpec("managed"), sink)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-sink.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("managed process did not produce its readiness marker")
	}
	if err := proc.Cancel(); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	_, err = proc.Wait()
	var canceled *CanceledError
	if !errors.As(err, &canceled) {
		t.Fatalf("Wait() error = %v, want CanceledError", err)
	}
}

func TestRunnerMapsDeadlineToTypedTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := NewRunner().Run(ctx, helperSpec("sleep"))
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("Run() error = %v, want TimeoutError", err)
	}
}

func TestManagedStdinSerializesWritesAndRejectsClosedPipe(t *testing.T) {
	sink := &recordingSink{ready: make(chan struct{})}
	proc, err := NewRunner().Start(context.Background(), helperSpec("managed"), sink)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-sink.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("managed process did not produce its readiness marker")
	}

	writeResults := make(chan error, 2)
	var wg sync.WaitGroup
	for _, value := range []string{"first\n", "second\n"} {
		value := value
		wg.Add(1)
		go func() {
			defer wg.Done()
			writeResults <- proc.WriteStdin(context.Background(), []byte(value))
		}()
	}
	wg.Wait()
	close(writeResults)
	for writeErr := range writeResults {
		if writeErr != nil {
			t.Fatalf("WriteStdin() error = %v", writeErr)
		}
	}
	if err := proc.CloseStdin(); err != nil {
		t.Fatalf("CloseStdin() error = %v", err)
	}
	if err := proc.CloseStdin(); err != nil {
		t.Fatalf("second CloseStdin() error = %v", err)
	}
	if err := proc.WriteStdin(context.Background(), []byte("after-close")); err == nil {
		t.Fatal("WriteStdin() after CloseStdin returned nil")
	} else {
		var stdinErr *StdinError
		if !errors.As(err, &stdinErr) {
			t.Fatalf("WriteStdin() after close error = %T, want StdinError", err)
		}
	}
	if _, err := proc.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	output := sink.String()
	if !strings.Contains(output, "first\n") || !strings.Contains(output, "second\n") {
		t.Fatalf("managed output = %q", output)
	}
}

func TestRunStreamHashesNearLimitOutput(t *testing.T) {
	payload := strings.Repeat("n", 8*1024)
	spec := helperSpec("stream", payload)
	spec.StdoutLimit = int64(len(payload))
	spec.StderrLimit = int64(len(payload))
	file, err := os.CreateTemp(t.TempDir(), "stream-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewRunner().RunStream(context.Background(), spec, file)
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("RunStream() error = %v", err)
	}
	wantHash := sha256.Sum256([]byte(payload))
	if result.StdoutBytes != int64(len(payload)) || result.StdoutHash != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("stream result = bytes %d hash %q", result.StdoutBytes, result.StdoutHash)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != payload {
		t.Fatalf("streamed file length = %d, want %d", len(data), len(payload))
	}
}

func TestRunStreamSinkFailureCancelsAndDrains(t *testing.T) {
	result, err := NewRunner().RunStream(context.Background(), helperSpec("verbose"), failingWriter{})
	var sinkErr *SinkError
	if !errors.As(err, &sinkErr) {
		t.Fatalf("RunStream() error = %v, want SinkError", err)
	}
	if result.StdoutHash != "" {
		t.Fatalf("failed stream published hash %q", result.StdoutHash)
	}
}

func TestEnvironmentPolicyUsesOnlyExplicitValues(t *testing.T) {
	const inherited = "NUDGE_RUNNER_INHERITED"
	if err := os.Setenv(inherited, "secret"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(inherited) })

	spec := helperSpec("env", inherited, "NUDGE_RUNNER_SET")
	spec.Environment.Set["NUDGE_RUNNER_SET"] = "minimal"
	result, err := NewRunner().Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err := json.Unmarshal(result.Stdout, &values); err != nil {
		t.Fatalf("decode minimal environment: %v; stdout=%q stderr=%q", err, result.Stdout, result.Stderr)
	}
	if values[inherited] != "" || values["NUDGE_RUNNER_SET"] != "minimal" {
		t.Fatalf("minimal environment values = %#v", values)
	}

	spec = helperSpec("env", inherited, "NUDGE_RUNNER_SET")
	spec.Environment = EnvironmentPolicy{
		Mode: EnvironmentInherit,
		Set: map[string]string{
			"NUDGE_PROCESS_HELPER": "1",
			"NUDGE_HELPER_MODE":    "env",
			"NUDGE_RUNNER_SET":     "overlay",
		},
		Remove: []string{inherited},
	}
	result, err = NewRunner().Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result.Stdout, &values); err != nil {
		t.Fatalf("decode inherited environment: %v; stdout=%q stderr=%q", err, result.Stdout, result.Stderr)
	}
	if values[inherited] != "" || values["NUDGE_RUNNER_SET"] != "overlay" {
		t.Fatalf("inherit/remove/set values = %#v", values)
	}
}

type recordingSink struct {
	mu    sync.Mutex
	data  strings.Builder
	ready chan struct{}
	once  sync.Once
}

func (s *recordingSink) Stdout(data []byte) error {
	s.mu.Lock()
	s.data.Write(data)
	s.mu.Unlock()
	if s.ready != nil && strings.Contains(string(data), "ready\n") {
		s.once.Do(func() { close(s.ready) })
	}
	return nil
}

func (s *recordingSink) Stderr(data []byte) error { return nil }

func (s *recordingSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.String()
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("sink failure") }
