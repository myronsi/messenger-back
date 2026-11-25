package messages

import (
	"errors"
	"math/rand/v2"

	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	importChunk      = 5000
	importNodes      = 16
	importWindowGaps = 2
)

var errNoRoom = errors.New("no room for the import")

type importLayout struct {
	windows []postgres.ImportWindow
}

func windowMillis(size int) int64 {
	return int64((size + ids.PerMilli - 1) / ids.PerMilli)
}

func planImport(lower, upper int64, positions int, rnd func(n int64) int64) (importLayout, error) {
	if positions <= 0 {
		return importLayout{}, errNoRoom
	}
	a := ids.Millis(lower) + 1
	b := ids.Millis(upper) - 1
	if b <= a {
		return importLayout{}, errNoRoom
	}
	b = a + (b-a)/2
	chunks := (positions + importChunk - 1) / importChunk
	slice := (b - a + 1) / int64(chunks)
	need := windowMillis(min(positions, importChunk)) + importWindowGaps
	if slice < need {
		return importLayout{}, errNoRoom
	}
	out := importLayout{windows: make([]postgres.ImportWindow, chunks)}
	for i := range chunks {
		size := min(importChunk, positions-i*importChunk)
		free := slice - windowMillis(size) - importWindowGaps
		start := a + int64(i)*slice + 1 + rnd(free+1)
		out.windows[i] = postgres.ImportWindow{Chunk: i, Node: ids.FirstImportNode + int(rnd(importNodes)), StartMS: start, Size: size}
	}
	return out, nil
}

func randomInt(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return rand.Int64N(n) //nolint:gosec // spreading, not security
}

func (l importLayout) id(position int) int64 {
	return positionID(l.windows, position)
}

func positionID(windows []postgres.ImportWindow, position int) int64 {
	w := windows[position/importChunk]
	within := position % importChunk
	return ids.Compose(w.StartMS+int64(within/ids.PerMilli), w.Node, within%ids.PerMilli)
}
