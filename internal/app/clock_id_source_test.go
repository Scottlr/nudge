package app

import (
	"testing"
	"time"
)

var (
	_ Clock    = SystemClock{}
	_ IDSource = RandomIDSource{}
)

type fixedClock struct {
	when time.Time
}

func (c fixedClock) Now() time.Time {
	return c.when
}

type fixedIDSource struct {
	id string
}

func (s fixedIDSource) NewID() string {
	return s.id
}

func TestApplicationSourcesCanBeInjectedDeterministically(t *testing.T) {
	wantTime := time.Date(2026, time.July, 14, 8, 45, 0, 0, time.UTC)
	clock := Clock(fixedClock{when: wantTime})
	if got := clock.Now(); !got.Equal(wantTime) {
		t.Fatalf("clock time = %v, want %v", got, wantTime)
	}

	source := IDSource(fixedIDSource{id: "fixed-id"})
	if got := source.NewID(); got != "fixed-id" {
		t.Fatalf("ID = %q, want %q", got, "fixed-id")
	}
}
