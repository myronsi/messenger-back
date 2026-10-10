// Package ids generates 64-bit Snowflake IDs: sortable by time and unique across instances without
// coordination, as long as every running instance has its own node number.
//
// Layout (most significant first): 1 unused sign bit, 41 bits of milliseconds since Epoch (about 69 years),
// 10 bits of node (0-1023) and 12 bits of sequence (4096 IDs per millisecond and node).
//
// v1 message IDs are serial numbers; every Snowflake ID is at least MinSnowflake (about 1.3e17), far above
// any serial number a v1 installation can reach, so v1 and v2 messages sort correctly together and
// IsSnowflake tells them apart. IDs go to clients as strings: they do not fit into a JavaScript number.
package ids

import (
	"errors"
	"sync"
	"time"
)

// Epoch is 2024-01-01T00:00:00Z. Changing it would break the ordering of existing IDs.
var Epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

const (
	nodeBits = 10
	seqBits  = 12
	// MaxNode is the largest node number.
	MaxNode   = 1<<nodeBits - 1
	maxSeq    = 1<<seqBits - 1
	timeShift = nodeBits + seqBits
)

// Cutover is the earliest time a Generator issues IDs for. IDs of earlier times (MinSnowflake and below) are
// never generated, which is what makes IsSnowflake exact.
var Cutover = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// MinSnowflake is the smallest ID a Generator can return.
var MinSnowflake = MinAt(Cutover)

// ErrClockBeforeCutover is returned when the system clock is set before Cutover.
var ErrClockBeforeCutover = errors.New("ids: clock is before the ID cutover")

// maxBackwards is how far the clock may jump back before Next refuses to hand out IDs instead of waiting.
const maxBackwards = 5 * time.Second

// ErrClockBackwards is returned when the system clock moved back by more than a few seconds; waiting that
// long inside a request is worse than failing it.
var ErrClockBackwards = errors.New("ids: clock moved backwards")

// ErrNotValid is returned after the validity set with ValidUntil has passed: the node number may belong to
// another process by now (its lease could not be renewed in time).
var ErrNotValid = errors.New("ids: the node number is no longer held")

// Generator hands out IDs for one node. It is safe for concurrent use.
type Generator struct {
	node uint64
	now  func() time.Time

	mu    sync.Mutex
	last  int64 // milliseconds since Epoch of the last ID
	seq   uint64
	valid time.Time // zero: no limit
}

// NewGenerator returns the generator of the node.
func NewGenerator(node int) (*Generator, error) {
	if node < 0 || node > MaxNode {
		return nil, errors.New("ids: node must be between 0 and 1023")
	}
	return &Generator{node: uint64(node), now: time.Now}, nil
}

func (g *Generator) millis() int64 { return g.now().Sub(Epoch).Milliseconds() }

var cutoverMillis = Cutover.Sub(Epoch).Milliseconds()

// Resume continues after the IDs a previous holder of the node issued up to lastMillis (milliseconds since
// Epoch, see LastMillis): the next ID is in a later millisecond, even if this clock is behind the previous
// holder's. Call it before the first Next.
func (g *Generator) Resume(lastMillis int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if lastMillis > g.last {
		g.last, g.seq = lastMillis, maxSeq
	}
}

// LastMillis is the millisecond of the newest ID issued (0 before the first), the value for Resume.
func (g *Generator) LastMillis() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

// ValidUntil makes Next fail after t, until it is extended. Leases call it on every renewal, so a process
// that was paused past its lease cannot hand out IDs another process may be using by now.
func (g *Generator) ValidUntil(t time.Time) {
	g.mu.Lock()
	g.valid = t
	g.mu.Unlock()
}

// Next returns a new ID, larger than every ID this generator returned before.
func (g *Generator) Next() (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.valid.IsZero() && g.now().After(g.valid) {
		return 0, ErrNotValid
	}
	ms := g.millis()
	if ms < cutoverMillis {
		return 0, ErrClockBeforeCutover
	}
	if ms < g.last {
		if time.Duration(g.last-ms)*time.Millisecond > maxBackwards {
			return 0, ErrClockBackwards
		}
		// A small step back (NTP adjustment, or IDs borrowed from the next milliseconds): keep counting in
		// the last millisecond.
		ms = g.last
	}
	switch {
	case ms > g.last:
		g.seq = 0
	case g.seq < maxSeq:
		g.seq++
	default:
		// 4096 IDs in this millisecond: borrow the next one. A sustained burst runs ahead of the clock,
		// and the clock catches up as soon as it eases (or Next fails after maxBackwards).
		ms = g.last + 1
		g.seq = 0
	}
	g.last = ms
	return int64(uint64(ms)<<timeShift | g.node<<seqBits | g.seq), nil //nolint:gosec // ms is positive and below 2^41
}

// Time returns when a Snowflake ID was generated (millisecond precision). It is meaningless for v1 IDs;
// see IsSnowflake.
func Time(id int64) time.Time {
	return Epoch.Add(time.Duration(id>>timeShift) * time.Millisecond)
}

// IsSnowflake reports whether the ID was generated by this package rather than being a v1 serial number.
func IsSnowflake(id int64) bool { return id >= MinSnowflake }

// MinAt returns the smallest ID that a generator could have produced at t; useful as a range bound.
func MinAt(t time.Time) int64 {
	ms := t.Sub(Epoch).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms << timeShift
}
