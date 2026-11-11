package clock

import "time"

// Clock allows deterministic tests without sleeping.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

func Real() Clock { return realClock{} }

type Fixed struct{ T time.Time }

func (f Fixed) Now() time.Time { return f.T }
