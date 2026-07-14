package process

import "time"

// Result is the bounded result of a finite in-memory process run.
type Result struct {
	ExitCode        int
	Stdout          []byte
	Stderr          []byte
	Duration        time.Duration
	StdoutTruncated bool
	StderrTruncated bool
}

// StreamResult is the bounded result of a finite streamed run or a managed
// process. A non-empty StdoutHash is only published for a complete successful
// finite stream; incomplete or failed streams leave it empty.
type StreamResult struct {
	ExitCode    int
	StdoutBytes int64
	StdoutHash  string
	StderrTail  []byte
	Duration    time.Duration
}
