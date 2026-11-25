package messages

import (
	"errors"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/ids"
)

func TestPlanImportKeepsOrderInsideTheGap(t *testing.T) {
	lower := ids.MinAt(time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC))
	upper := ids.MinAt(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	for _, positions := range []int{1, 2, 4097, importChunk, importChunk + 1, 3*importChunk + 17, MaxImportMessages + 2} {
		for _, rnd := range []func(int64) int64{func(int64) int64 { return 0 }, func(n int64) int64 { return n - 1 }, randomInt} {
			layout, err := planImport(lower, upper, positions, rnd)
			if err != nil {
				t.Fatalf("%d positions: %v", positions, err)
			}
			prev := lower
			for p := range positions {
				id := layout.id(p)
				if id <= prev {
					t.Fatalf("%d positions: id of %d is %d, not above %d", positions, p, id, prev)
				}
				prev = id
			}
			if prev >= upper {
				t.Fatalf("%d positions: last id %d not below %d", positions, prev, upper)
			}
			for _, w := range layout.windows {
				if w.Node < ids.FirstImportNode || w.Node > ids.MaxNode || w.Size < 1 || w.Size > importChunk {
					t.Fatalf("bad window %+v", w)
				}
			}
		}
	}
}

func TestPlanImportUsesOnlyTheLowerHalf(t *testing.T) {
	lower := ids.MinAt(time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC))
	upper := ids.MinAt(time.Date(2025, 3, 21, 0, 0, 0, 0, time.UTC))
	layout, err := planImport(lower, upper, 20000, randomInt)
	if err != nil {
		t.Fatal(err)
	}
	middle := lower + (upper-lower)/2
	if last := layout.id(19999); last >= middle+ids.MinAt(ids.Epoch.Add(time.Millisecond)) {
		t.Fatalf("last id %d is in the upper half (middle %d)", last, middle)
	}
}

func TestPlanImportNeedsRoom(t *testing.T) {
	lower := ids.MinAt(time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC))
	cases := map[string]struct {
		upper     int64
		positions int
	}{
		"same millisecond":   {lower + 5, 3},
		"two milliseconds":   {ids.MinAt(ids.Time(lower).Add(2 * time.Millisecond)), 3},
		"too many for 50 ms": {ids.MinAt(ids.Time(lower).Add(50 * time.Millisecond)), 30 * importChunk},
		"nothing to place":   {ids.MinAt(ids.Time(lower).Add(time.Hour)), 0},
	}
	for name, c := range cases {
		if _, err := planImport(lower, c.upper, c.positions, randomInt); !errors.Is(err, errNoRoom) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPositionIDIsStable(t *testing.T) {
	lower := ids.MinAt(time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC))
	upper := ids.MinAt(time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC))
	layout, err := planImport(lower, upper, 12000, randomInt)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []int{0, 4095, 4096, 4999, 5000, 11999} {
		if a, b := layout.id(p), positionID(layout.windows, p); a != b || !ids.IsSnowflake(a) {
			t.Fatalf("position %d: %d and %d", p, a, b)
		}
	}
}
