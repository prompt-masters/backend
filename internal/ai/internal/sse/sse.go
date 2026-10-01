// Package sse reads Server-Sent Events incrementally: each event is returned
// as soon as its terminating blank line arrives, never after buffering the
// whole body.
package sse

import (
	"bufio"
	"io"
	"strings"
)

// maxLineBytes bounds one SSE line.
const maxLineBytes = 1 << 20

type Event struct {
	// Name is the event field, or "" when the event has none.
	Name string
	Data string
}

type Reader struct {
	scanner *bufio.Scanner
}

func NewReader(r io.Reader) *Reader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	return &Reader{scanner: s}
}

// Next returns the next event. It returns io.EOF once the body ends cleanly
// and the read error otherwise.
func (r *Reader) Next() (Event, error) {
	var (
		ev   Event
		data []string
		seen bool
	)
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if line == "" {
			if seen {
				ev.Data = strings.Join(data, "\n")
				return ev, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") { // comment / keep-alive
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			ev.Name, seen = value, true
		case "data":
			data, seen = append(data, value), true
		}
	}
	if err := r.scanner.Err(); err != nil {
		return Event{}, err
	}
	if seen {
		ev.Data = strings.Join(data, "\n")
		return ev, nil
	}
	return Event{}, io.EOF
}
