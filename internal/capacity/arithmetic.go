// Package capacity contains checked, platform-independent capacity arithmetic.
// It deliberately has no filesystem, database, or operating-system concerns.
package capacity

import (
	"errors"
	"math"
)

var (
	// ErrInvalid reports an invalid capacity quantity or empty arithmetic input.
	ErrInvalid = errors.New("invalid capacity quantity")
	// ErrOverflow reports checked capacity arithmetic overflow.
	ErrOverflow = errors.New("capacity arithmetic overflow")
)

// Bytes is a non-negative byte quantity used by capacity calculations.
type Bytes uint64

// Add returns the checked sum of all values.
func Add(values ...Bytes) (Bytes, error) {
	if len(values) == 0 {
		return 0, ErrInvalid
	}
	var total Bytes
	for _, value := range values {
		if uint64(value) > math.MaxUint64-uint64(total) {
			return 0, ErrOverflow
		}
		total += value
	}
	return total, nil
}

// Mul returns the checked product of value and count.
func Mul(value Bytes, count uint64) (Bytes, error) {
	if count != 0 && uint64(value) > math.MaxUint64/count {
		return 0, ErrOverflow
	}
	return Bytes(uint64(value) * count), nil
}

// VolumePeak is the independent charge for one volume. Reserve is included
// in Charge so the caller cannot accidentally omit the protected reserve.
type VolumePeak struct {
	Inputs                 Bytes
	Temporaries            Bytes
	Finals                 Bytes
	CopyOnWrite            Bytes
	DatabaseWAL            Bytes
	AtomicOutput           Bytes
	ConcurrentReservations Bytes
	Reserve                Bytes
	RetainedDelta          Bytes
}

// Charge sums every peak class, including the protected reserve.
func (p VolumePeak) Charge() (Bytes, error) {
	return Add(p.Inputs, p.Temporaries, p.Finals, p.CopyOnWrite, p.DatabaseWAL, p.AtomicOutput, p.ConcurrentReservations, p.Reserve, p.RetainedDelta)
}
