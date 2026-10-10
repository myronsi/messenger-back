package ids

import (
	"sync"
	"testing"
	"time"
)

func TestNodeRange(t *testing.T) {
	for _, n := range []int{-1, MaxNode + 1} {
		if _, err := NewGenerator(n); err == nil {
			t.Fatalf("accepted node %d", n)
		}
	}
}

func TestIDsIncreaseAndCarryTimeAndNode(t *testing.T) {
	g, err := NewGenerator(513)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Millisecond)
	var last int64
	for range 20000 { // several milliseconds' worth, including sequence overflows
		id, err := g.Next()
		if err != nil {
			t.Fatal(err)
		}
		if id <= last {
			t.Fatalf("%d after %d", id, last)
		}
		last = id
	}
	if !IsSnowflake(last) || IsSnowflake(4_000_000) || IsSnowflake(MinSnowflake-1) {
		t.Fatal("IsSnowflake")
	}
	if ts := Time(last); ts.Before(before) || ts.After(time.Now().Add(time.Second)) {
		t.Fatalf("time %v", ts)
	}
	if node := (last >> seqBits) & MaxNode; node != 513 {
		t.Fatalf("node %d", node)
	}
	if MinAt(Time(last)) > last || MinAt(Time(last).Add(time.Millisecond)) <= last-maxSeq-1 {
		t.Fatal("MinAt")
	}
	if MinAt(Epoch.Add(-time.Hour)) != 0 {
		t.Fatal("MinAt before Epoch")
	}
}

func TestNoIDsBeforeCutover(t *testing.T) {
	g, _ := NewGenerator(1)
	g.now = func() time.Time { return Cutover.Add(-time.Millisecond) }
	if _, err := g.Next(); err == nil {
		t.Fatal("generated an ID before the cutover")
	}
	g.now = func() time.Time { return Cutover }
	id, err := g.Next()
	if err != nil || id != MinSnowflake+(1<<seqBits) || !IsSnowflake(id) {
		t.Fatalf("first ID at the cutover: %d %v", id, err)
	}
}

func TestClockGoingBackwards(t *testing.T) {
	g, _ := NewGenerator(1)
	now := time.Now()
	g.now = func() time.Time { return now }
	a, _ := g.Next()
	now = now.Add(-time.Second) // small step back: IDs keep increasing
	b, err := g.Next()
	if err != nil || b <= a {
		t.Fatalf("small step back: %d %d %v", a, b, err)
	}
	now = now.Add(-time.Minute)
	if _, err := g.Next(); err == nil {
		t.Fatal("large step back accepted")
	}
}

func TestUniqueAcrossGoroutinesAndNodes(t *testing.T) {
	seen := sync.Map{}
	var wg sync.WaitGroup
	for node := range 4 {
		g, _ := NewGenerator(node)
		for range 4 {
			wg.Go(func() {
				for range 5000 {
					id, err := g.Next()
					if err != nil {
						t.Error(err)
						return
					}
					if _, dup := seen.LoadOrStore(id, true); dup {
						t.Errorf("duplicate %d", id)
						return
					}
				}
			})
		}
	}
	wg.Wait()
}
