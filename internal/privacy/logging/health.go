// Package logging implements Nudge's protected operational log sink.
package logging

import (
	"sync"

	"github.com/Scottlr/nudge/internal/app"
)

type healthState struct {
	mu       sync.Mutex
	disabled bool
	count    app.Count
	last     app.LogFailureCode
}

func (h *healthState) fail(code app.LogFailureCode) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.disabled = true
	if h.count < app.Count(^uint64(0)) {
		h.count++
	}
	h.last = code
	h.mu.Unlock()
}

func (h *healthState) reject() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.count < app.Count(^uint64(0)) {
		h.count++
	}
	h.last = app.LogFailureRejected
	h.mu.Unlock()
}

func (h *healthState) snapshot() app.LogHealth {
	if h == nil {
		return app.LogHealth{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return app.LogHealth{Disabled: h.disabled, FailureCount: h.count, LastFailure: h.last}
}
