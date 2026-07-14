package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultReadChunk = 32 * 1024
	processWaitDelay = 500 * time.Millisecond
)

// EnvironmentMode controls the source of a child environment.
type EnvironmentMode string

const (
	// EnvironmentMinimal starts with no inherited variables.
	EnvironmentMinimal EnvironmentMode = "minimal"
	// EnvironmentInherit starts with the current environment, then applies
	// Remove and Set explicitly.
	EnvironmentInherit EnvironmentMode = "inherit"
)

// EnvironmentPolicy is deliberately explicit. Set never causes a value to be
// inherited when Mode is EnvironmentMinimal, and Remove is applied before Set.
type EnvironmentPolicy struct {
	Mode   EnvironmentMode
	Set    map[string]string
	Remove []string
}

// Spec describes one direct child-process invocation. Executable and Args are
// passed to os/exec as separate values and are never interpreted by a shell.
type Spec struct {
	Executable  string
	Args        []string
	Dir         string
	Environment EnvironmentPolicy
	Stdin       io.Reader
	Timeout     time.Duration
	StdoutLimit int64
	StderrLimit int64
}

// StreamSink receives managed-process output. One runner-owned dispatcher
// invokes each method serially; ordering between stdout and stderr is not
// specified. The byte slice is only valid for the duration of the callback.
type StreamSink interface {
	Stdout([]byte) error
	Stderr([]byte) error
}

// Runner is the single child-process boundary used by Git and Codex adapters.
type Runner interface {
	Run(ctx context.Context, spec Spec) (Result, error)
	RunStream(ctx context.Context, spec Spec, stdout io.Writer) (StreamResult, error)
	Start(ctx context.Context, spec Spec, sink StreamSink) (Process, error)
}

// Process is a managed-duplex child process. Wait joins all runner-owned
// readers and the native process-tree controller. Callers must close stdin
// when the child protocol expects EOF.
type Process interface {
	WriteStdin(ctx context.Context, data []byte) error
	CloseStdin() error
	Wait() (StreamResult, error)
	Cancel() error
}

// DefaultRunner is the production implementation of Runner.
type DefaultRunner struct{}

// NewRunner constructs the bounded process runner.
func NewRunner() Runner { return &DefaultRunner{} }

var _ Runner = (*DefaultRunner)(nil)

func (r *DefaultRunner) Run(ctx context.Context, spec Spec) (Result, error) {
	if err := validateSpec(spec); err != nil {
		return Result{}, err
	}
	cmd, stdout, stderr, err := startPipedCommand(ctx, spec, false)
	if err != nil {
		return Result{}, err
	}

	failures := &failureRecorder{}
	stdoutDone := make(chan finiteOutput, 1)
	stderrDone := make(chan finiteOutput, 1)
	go func() {
		stdoutDone <- readFiniteStdout(stdout, spec.StdoutLimit, failures, cmd.stop)
	}()
	go func() {
		stderrDone <- readFiniteStderr(stderr, spec.StderrLimit, failures, cmd.stop)
	}()

	waitErr := cmd.wait()
	stdoutResult := <-stdoutDone
	stderrResult := <-stderrDone
	cmd.finish()

	result := Result{
		ExitCode:        exitCode(cmd.cmd),
		Stdout:          stdoutResult.data,
		Stderr:          stderrResult.data,
		Duration:        time.Since(cmd.started),
		StdoutTruncated: stdoutResult.truncated,
		StderrTruncated: stderrResult.truncated,
	}
	return result, commandError(cmd, waitErr, failures.err())
}

func (r *DefaultRunner) RunStream(ctx context.Context, spec Spec, stdout io.Writer) (StreamResult, error) {
	if stdout == nil {
		return StreamResult{}, invalid("stdout sink")
	}
	if err := validateSpec(spec); err != nil {
		return StreamResult{}, err
	}
	cmd, stdoutPipe, stderrPipe, err := startPipedCommand(ctx, spec, false)
	if err != nil {
		return StreamResult{}, err
	}

	failures := &failureRecorder{}
	stdoutDone := make(chan finiteOutput, 1)
	stderrDone := make(chan finiteOutput, 1)
	go func() {
		stdoutDone <- streamStdout(stdoutPipe, stdout, spec.StdoutLimit, failures, cmd.stop)
	}()
	go func() {
		stderrDone <- readFiniteStderr(stderrPipe, spec.StderrLimit, failures, cmd.stop)
	}()

	waitErr := cmd.wait()
	stdoutResult := <-stdoutDone
	stderrResult := <-stderrDone
	cmd.finish()

	result := StreamResult{
		ExitCode:    exitCode(cmd.cmd),
		StdoutBytes: stdoutResult.bytes,
		StderrTail:  stderrResult.data,
		Duration:    time.Since(cmd.started),
	}
	processErr := commandError(cmd, waitErr, failures.err())
	if processErr == nil {
		result.StdoutHash = hex.EncodeToString(stdoutResult.hash.Sum(nil))
	}
	return result, processErr
}

func (r *DefaultRunner) Start(ctx context.Context, spec Spec, sink StreamSink) (Process, error) {
	if sink == nil {
		return nil, invalid("stream sink")
	}
	if spec.Stdin != nil {
		return nil, invalid("managed stdin")
	}
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	cmd, stdout, stderr, err := startPipedCommand(ctx, spec, true)
	if err != nil {
		return nil, err
	}

	failures := &failureRecorder{}
	stdoutDone := make(chan managedOutput, 1)
	stderrDone := make(chan managedOutput, 1)
	go func() {
		stdoutDone <- readManaged(stdout, StreamStdout, sink.Stdout, 0, failures, cmd.stop)
	}()
	go func() {
		stderrDone <- readManaged(stderr, StreamStderr, sink.Stderr, spec.StderrLimit, failures, cmd.stop)
	}()

	return &managedProcess{
		command:    cmd,
		failures:   failures,
		stdoutDone: stdoutDone,
		stderrDone: stderrDone,
	}, nil
}

func validateSpec(spec Spec) error {
	if spec.Executable == "" {
		return invalid("executable")
	}
	if strings.IndexByte(spec.Executable, 0) >= 0 {
		return invalid("executable")
	}
	for _, arg := range spec.Args {
		if strings.IndexByte(arg, 0) >= 0 {
			return invalid("argument")
		}
	}
	if strings.IndexByte(spec.Dir, 0) >= 0 {
		return invalid("working directory")
	}
	if spec.Dir != "" {
		info, err := os.Stat(spec.Dir)
		if err != nil || !info.IsDir() {
			return invalid("working directory")
		}
	}
	if spec.Timeout < 0 {
		return invalid("timeout")
	}
	if spec.StdoutLimit <= 0 {
		return invalid("stdout limit")
	}
	if spec.StderrLimit <= 0 {
		return invalid("stderr limit")
	}
	if err := validateEnvironment(spec.Environment); err != nil {
		return err
	}
	return nil
}

func validateEnvironment(policy EnvironmentPolicy) error {
	if policy.Mode != "" && policy.Mode != EnvironmentMinimal && policy.Mode != EnvironmentInherit {
		return invalid("environment mode")
	}
	for key, value := range policy.Set {
		if err := validateEnvironmentKey(key); err != nil {
			return err
		}
		if strings.IndexByte(value, 0) >= 0 {
			return invalid("environment value")
		}
	}
	for _, key := range policy.Remove {
		if err := validateEnvironmentKey(key); err != nil {
			return err
		}
	}
	return nil
}

func validateEnvironmentKey(key string) error {
	if key == "" || strings.IndexByte(key, 0) >= 0 || strings.ContainsRune(key, '=') {
		return invalid("environment key")
	}
	return nil
}

func buildEnvironment(policy EnvironmentPolicy) []string {
	values := make(map[string]string)
	if policy.Mode == EnvironmentInherit {
		for _, entry := range os.Environ() {
			key, value, ok := strings.Cut(entry, "=")
			if ok {
				values[environmentLookupKey(key)] = key + "=" + value
			}
		}
	}
	for _, key := range policy.Remove {
		delete(values, environmentLookupKey(key))
	}
	for key, value := range policy.Set {
		values[environmentLookupKey(key)] = key + "=" + value
	}

	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func environmentLookupKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

type commandState struct {
	cmd     *exec.Cmd
	tree    processTree
	cancel  context.CancelFunc
	ctx     context.Context
	started time.Time
	stdin   io.WriteCloser

	mu       sync.Mutex
	stopped  bool
	manual   bool
	finished bool
}

func startPipedCommand(ctx context.Context, spec Spec, managed bool) (*commandState, io.ReadCloser, io.ReadCloser, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, contextFailure(err)
	}

	childCtx, cancel := context.WithCancel(ctx)
	var timeoutCancel context.CancelFunc
	if spec.Timeout > 0 {
		childCtx, timeoutCancel = context.WithTimeout(childCtx, spec.Timeout)
	}
	stopContext := func() {
		if timeoutCancel != nil {
			timeoutCancel()
		}
		cancel()
	}

	cmd := exec.CommandContext(childCtx, spec.Executable, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = buildEnvironment(spec.Environment)
	cmd.WaitDelay = processWaitDelay
	if !managed {
		cmd.Stdin = spec.Stdin
	}
	var err error
	var stdin io.WriteCloser
	if managed {
		stdin, err = cmd.StdinPipe()
		if err != nil {
			stopContext()
			return nil, nil, nil, &SpawnError{Cause: err}
		}
	}

	tree := newProcessTree()
	if err := tree.Prepare(cmd); err != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		stopContext()
		_ = tree.Close()
		return nil, nil, nil, &SpawnError{Cause: err}
	}
	cmd.Cancel = tree.Cancel
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		stopContext()
		_ = tree.Close()
		return nil, nil, nil, &SpawnError{Cause: err}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		if stdin != nil {
			_ = stdin.Close()
		}
		stopContext()
		_ = tree.Close()
		return nil, nil, nil, &SpawnError{Cause: err}
	}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		if stdin != nil {
			_ = stdin.Close()
		}
		stopContext()
		_ = tree.Close()
		return nil, nil, nil, &SpawnError{Cause: err}
	}
	if err := tree.Attach(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = stdout.Close()
		_ = stderr.Close()
		if stdin != nil {
			_ = stdin.Close()
		}
		stopContext()
		_ = tree.Close()
		return nil, nil, nil, &SpawnError{Cause: err}
	}

	state := &commandState{cmd: cmd, tree: tree, cancel: stopContext, ctx: childCtx, stdin: stdin, started: time.Now()}
	if err := childCtx.Err(); err != nil {
		state.stop()
	}
	return state, stdout, stderr, nil
}

func (c *commandState) stop() {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.stopped = true
	stdin := c.stdin
	c.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	c.cancel()
	_ = c.tree.Cancel()
}

func (c *commandState) userCancel() {
	c.mu.Lock()
	c.manual = true
	c.mu.Unlock()
	c.stop()
}

func (c *commandState) wait() error { return c.cmd.Wait() }

func (c *commandState) finish() {
	c.mu.Lock()
	c.finished = true
	stdin := c.stdin
	c.stdin = nil
	c.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	_ = c.tree.Close()
}

func (c *commandState) wasManuallyCanceled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.manual
}

func commandError(cmd *commandState, waitErr error, streamErr error) error {
	if streamErr != nil {
		return streamErr
	}
	if waitErr == nil {
		return nil
	}
	if cmd.wasManuallyCanceled() {
		return contextFailure(context.Canceled)
	}
	if cause := cmd.cmdContextErr(); cause != nil {
		return contextFailure(cause)
	}
	return &ExitError{ExitCode: exitCode(cmd.cmd), Cause: waitErr}
}

func (c *commandState) cmdContextErr() error {
	return c.ctx.Err()
}

func exitCode(cmd *exec.Cmd) int {
	if cmd == nil || cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

type failureRecorder struct {
	mu    sync.Mutex
	value error
}

func (f *failureRecorder) set(err error) {
	if err == nil {
		return
	}
	f.mu.Lock()
	if f.value == nil {
		f.value = err
	}
	f.mu.Unlock()
}

func (f *failureRecorder) err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value
}

type finiteOutput struct {
	data      []byte
	bytes     int64
	hash      hashAccumulator
	truncated bool
}

type hashAccumulator struct{ hash [sha256.Size]byte }

func (h hashAccumulator) Sum(_ []byte) []byte {
	result := make([]byte, len(h.hash))
	copy(result, h.hash[:])
	return result
}

type tailBuffer struct {
	limit     int64
	data      []byte
	bytes     int64
	truncated bool
}

func (b *tailBuffer) add(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	if int64(len(chunk)) > maxInt64-b.bytes {
		b.bytes = maxInt64
	} else {
		b.bytes += int64(len(chunk))
	}
	if b.limit <= 0 {
		b.truncated = true
		return
	}
	if int64(len(chunk)) > b.limit {
		b.data = append(b.data[:0], chunk[len(chunk)-int(b.limit):]...)
		b.truncated = true
		return
	}
	combined := append(b.data, chunk...)
	if int64(len(combined)) > b.limit {
		combined = combined[len(combined)-int(b.limit):]
		b.truncated = true
	}
	b.data = append(b.data[:0], combined...)
}

const maxInt64 = int64(^uint64(0) >> 1)

func overLimit(limit int64) int64 {
	if limit == maxInt64 {
		return maxInt64
	}
	return limit + 1
}

func addBytes(current int64, added int) int64 {
	if added <= 0 {
		return current
	}
	if current >= maxInt64-int64(added) {
		return maxInt64
	}
	return current + int64(added)
}

func readFiniteStdout(reader io.Reader, limit int64, failures *failureRecorder, stop func()) finiteOutput {
	var result finiteOutput
	buf := make([]byte, defaultReadChunk)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if result.truncated {
				// Keep draining without retaining more output after the hard cap.
			} else if int64(n) > limit-result.bytes {
				remaining := limit - result.bytes
				if remaining > 0 {
					result.data = append(result.data, buf[:int(remaining)]...)
					result.bytes += remaining
				}
				result.truncated = true
				failures.set(&LimitError{Stream: StreamStdout, Limit: limit, Observed: overLimit(limit)})
				stop()
			} else if !result.truncated {
				result.data = append(result.data, buf[:n]...)
				result.bytes += int64(n)
			}
		}
		if err != nil {
			break
		}
	}
	return result
}

func readFiniteStderr(reader io.Reader, limit int64, failures *failureRecorder, stop func()) finiteOutput {
	var result finiteOutput
	tail := tailBuffer{limit: limit}
	buf := make([]byte, defaultReadChunk)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			before := tail.bytes
			tail.add(buf[:n])
			result.bytes = tail.bytes
			if tail.bytes > limit && before <= limit {
				result.truncated = true
				failures.set(&LimitError{Stream: StreamStderr, Limit: limit, Observed: overLimit(limit)})
				stop()
			}
		}
		if err != nil {
			break
		}
	}
	result.data = tail.data
	result.truncated = result.truncated || tail.truncated
	return result
}

func streamStdout(reader io.Reader, writer io.Writer, limit int64, failures *failureRecorder, stop func()) finiteOutput {
	var result finiteOutput
	hash := sha256.New()
	buf := make([]byte, defaultReadChunk)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			remaining := limit - result.bytes
			if remaining <= 0 {
				result.truncated = true
				failures.set(&LimitError{Stream: StreamStdout, Limit: limit, Observed: addBytes(result.bytes, n)})
				stop()
			} else {
				if int64(len(chunk)) > remaining {
					chunk = chunk[:int(remaining)]
					result.truncated = true
				}
				written, writeErr := writer.Write(chunk)
				if written < 0 || written > len(chunk) {
					written = 0
					writeErr = errors.New("invalid sink write count")
				}
				if written > 0 {
					result.bytes += int64(written)
					_, _ = hash.Write(chunk[:written])
				}
				if writeErr == nil && written != len(chunk) {
					writeErr = io.ErrShortWrite
				}
				if writeErr != nil {
					failures.set(&SinkError{Stream: StreamStdout, Cause: writeErr})
					stop()
				} else if result.truncated {
					failures.set(&LimitError{Stream: StreamStdout, Limit: limit, Observed: overLimit(limit)})
					stop()
				}
			}
		}
		if err != nil {
			break
		}
	}
	copy(result.hash.hash[:], hash.Sum(nil))
	return result
}

type managedOutput struct {
	bytes    int64
	hash     hashAccumulator
	stderr   []byte
	callback bool
}

func readManaged(reader io.Reader, stream Stream, callback func([]byte) error, tailLimit int64, failures *failureRecorder, stop func()) managedOutput {
	var result managedOutput
	tail := tailBuffer{limit: tailLimit}
	hash := sha256.New()
	buf := make([]byte, defaultReadChunk)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			result.bytes = addBytes(result.bytes, n)
			if stream == StreamStdout {
				_, _ = hash.Write(chunk)
			} else {
				// Start has no lifetime output cap; retain only a small diagnostic
				// tail governed by the caller's bounded stream contract.
				tail.add(chunk)
			}
			if callback != nil {
				result.callback = true
				if callbackErr := callback(chunk); callbackErr != nil {
					failures.set(&SinkError{Stream: stream, Cause: callbackErr})
					stop()
					callback = nil
				}
			}
		}
		if err != nil {
			break
		}
	}
	copy(result.hash.hash[:], hash.Sum(nil))
	result.stderr = tail.data
	return result
}

type managedProcess struct {
	command    *commandState
	failures   *failureRecorder
	stdoutDone <-chan managedOutput
	stderrDone <-chan managedOutput
	writeMu    sync.Mutex

	waitOnce sync.Once
	waitDone chan struct{}
	result   StreamResult
	err      error
}

func (p *managedProcess) WriteStdin(ctx context.Context, data []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return contextFailure(err)
	}
	p.command.mu.Lock()
	stdin := p.command.stdin
	p.command.mu.Unlock()
	if stdin == nil {
		return &StdinError{}
	}

	// Only one writer owns the pipe at a time. Each bounded chunk gets one
	// cancellable write operation; closing the pipe is the wake-up for a child
	// that stopped reading.
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.writeSerialized(ctx, stdin, data)
}

func (p *managedProcess) writeSerialized(ctx context.Context, stdin io.Writer, data []byte) error {
	return p.writeData(ctx, stdin, data)
}

func (p *managedProcess) writeData(ctx context.Context, stdin io.Writer, data []byte) error {
	for len(data) > 0 {
		chunk := data
		if len(chunk) > defaultReadChunk {
			chunk = chunk[:defaultReadChunk]
		}
		type writeResult struct {
			n   int
			err error
		}
		result := make(chan writeResult, 1)
		go func() {
			n, err := stdin.Write(chunk)
			result <- writeResult{n: n, err: err}
		}()
		select {
		case <-ctx.Done():
			_ = p.CloseStdin()
			<-result
			return contextFailure(ctx.Err())
		case value := <-result:
			if value.n < 0 || value.n > len(chunk) {
				return &StdinError{Cause: errors.New("invalid stdin write count")}
			}
			if value.err != nil {
				return &StdinError{Cause: value.err}
			}
			if value.n != len(chunk) {
				return &StdinError{Cause: io.ErrShortWrite}
			}
			data = data[len(chunk):]
		}
	}
	return nil
}

func (p *managedProcess) CloseStdin() error {
	p.command.mu.Lock()
	stdin := p.command.stdin
	p.command.stdin = nil
	p.command.mu.Unlock()
	if stdin == nil {
		return nil
	}
	return stdin.Close()
}

func (p *managedProcess) Wait() (StreamResult, error) {
	p.waitOnce.Do(func() {
		p.waitDone = make(chan struct{})
		waitErr := p.command.wait()
		stdout := <-p.stdoutDone
		stderr := <-p.stderrDone
		p.command.finish()
		p.result = StreamResult{
			ExitCode:    exitCode(p.command.cmd),
			StdoutBytes: stdout.bytes,
			StderrTail:  stderr.stderr,
			Duration:    time.Since(p.command.started),
		}
		p.err = commandError(p.command, waitErr, p.failures.err())
		if p.err == nil {
			p.result.StdoutHash = hex.EncodeToString(stdout.hash.Sum(nil))
		}
		close(p.waitDone)
	})
	<-p.waitDone
	return p.result, p.err
}

func (p *managedProcess) Cancel() error {
	p.command.userCancel()
	return nil
}
