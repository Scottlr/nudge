// Package app owns application contracts and canonical workflow state.
package app

import "time"

// Clock supplies the current time to application behavior.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the current UTC time from the operating system.
type SystemClock struct{}

// Now returns the current time normalized to UTC.
func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}
