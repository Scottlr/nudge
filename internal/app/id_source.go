package app

import "crypto/rand"

// IDSource supplies opaque application identities.
type IDSource interface {
	NewID() string
}

// RandomIDSource produces opaque IDs using the Go standard library's
// cryptographic random text source. Callers must not infer an alphabet,
// length, ordering, or other structure from the returned value.
type RandomIDSource struct{}

// NewID returns a new opaque random identity.
func (RandomIDSource) NewID() string {
	return rand.Text()
}
