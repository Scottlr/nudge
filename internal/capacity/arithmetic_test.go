package capacity

import (
	"math"
	"testing"
)

func TestVolumePeakChargeIncludesEveryClass(t *testing.T) {
	charge, err := (VolumePeak{
		Inputs:                 1,
		Temporaries:            2,
		Finals:                 3,
		CopyOnWrite:            9,
		DatabaseWAL:            4,
		AtomicOutput:           5,
		ConcurrentReservations: 6,
		Reserve:                7,
		RetainedDelta:          8,
	}).Charge()
	if err != nil {
		t.Fatalf("Charge() error = %v", err)
	}
	if charge != 45 {
		t.Fatalf("Charge() = %d, want 45", charge)
	}
}

func TestAddAndMulRejectOverflow(t *testing.T) {
	if _, err := Add(Bytes(math.MaxUint64), 1); err != ErrOverflow {
		t.Fatalf("Add() error = %v, want ErrOverflow", err)
	}
	if _, err := Mul(Bytes(math.MaxUint64), 2); err != ErrOverflow {
		t.Fatalf("Mul() error = %v, want ErrOverflow", err)
	}
}
