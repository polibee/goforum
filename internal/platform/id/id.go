package id

import (
	"github.com/oklog/ulid/v2"
)

// New returns a time-sortable ULID string (26 chars).
func New() string { return ulid.Make().String() }

func Valid(s string) bool {
	_, err := ulid.ParseStrict(s)
	return err == nil
}
