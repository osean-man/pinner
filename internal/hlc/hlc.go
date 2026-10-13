// Package hlc implements a hybrid logical clock.
//
// Timestamps are encoded as fixed-width strings so that comparing two encoded timestamps
// lexically gives the same order as comparing them logically. That lets SQLite and Go
// compare them with plain string comparison.
package hlc

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Timestamp is a point in hybrid logical time. Node breaks ties between devices.
type Timestamp struct {
	Wall    int64 // unix milliseconds
	Counter uint32
	Node    string
}

// String encodes the timestamp as "<16 hex wall>.<8 hex counter>.<node>".
func (t Timestamp) String() string {
	return fmt.Sprintf("%016x.%08x.%s", t.Wall, t.Counter, t.Node)
}

// IsZero reports whether t is the zero timestamp.
func (t Timestamp) IsZero() bool {
	return t.Wall == 0 && t.Counter == 0 && t.Node == ""
}

// Parse decodes a timestamp produced by Timestamp.String.
func Parse(s string) (Timestamp, error) {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 || len(parts[0]) != 16 || len(parts[1]) != 8 || parts[2] == "" {
		return Timestamp{}, fmt.Errorf("invalid hlc timestamp %q", s)
	}
	wall, err := strconv.ParseInt(parts[0], 16, 64)
	if err != nil {
		return Timestamp{}, fmt.Errorf("invalid hlc wall time %q: %w", s, err)
	}
	counter, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return Timestamp{}, fmt.Errorf("invalid hlc counter %q: %w", s, err)
	}
	return Timestamp{Wall: wall, Counter: uint32(counter), Node: parts[2]}, nil
}

// Compare returns -1, 0 or 1 comparing a and b.
func Compare(a, b Timestamp) int {
	return strings.Compare(a.String(), b.String())
}

// Clock issues monotonically increasing timestamps for one node.
type Clock struct {
	mu   sync.Mutex
	node string
	last Timestamp
	now  func() time.Time
}

// NewClock returns a clock for node that never issues a timestamp at or before last.
func NewClock(node string, last Timestamp) *Clock {
	return &Clock{node: node, last: last, now: time.Now}
}

// SetNow replaces the clock's time source. It exists for tests that need to simulate skew.
func (c *Clock) SetNow(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// Now returns a new timestamp greater than every timestamp issued or observed so far.
func (c *Clock) Now() Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	wall := c.now().UnixMilli()
	if wall > c.last.Wall {
		c.last = Timestamp{Wall: wall, Node: c.node}
	} else {
		c.last = Timestamp{Wall: c.last.Wall, Counter: c.last.Counter + 1, Node: c.node}
	}
	return c.last
}

// Observe merges a timestamp seen from another node, so later local timestamps sort after it.
func (c *Clock) Observe(remote Timestamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if remote.Wall > c.last.Wall || (remote.Wall == c.last.Wall && remote.Counter > c.last.Counter) {
		c.last = Timestamp{Wall: remote.Wall, Counter: remote.Counter, Node: c.node}
	}
}

// Last returns the most recent timestamp issued or observed.
func (c *Clock) Last() Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}
