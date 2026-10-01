package service

import "time"

// Clock is the game engine's source of time and timers. Tests substitute a
// fake that controls both what time it is and when timers fire.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine once d has elapsed.
	AfterFunc(d time.Duration, f func()) Timer
}

type Timer interface {
	// Stop prevents the timer from firing, reporting whether it stopped it.
	Stop() bool
}

// SystemClock is the server's real clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

func (SystemClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
