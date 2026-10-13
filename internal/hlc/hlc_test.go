package hlc

import (
	"sort"
	"testing"
	"time"
)

func fixed(ms int64) func() time.Time {
	return func() time.Time { return time.UnixMilli(ms) }
}

func TestNowIsStrictlyIncreasingWhenWallClockStalls(t *testing.T) {
	c := NewClock("node-a", Timestamp{})
	c.SetNow(fixed(1000))
	prev := c.Now()
	for i := 0; i < 100; i++ {
		next := c.Now()
		if Compare(next, prev) <= 0 {
			t.Fatalf("timestamp %s not after %s", next, prev)
		}
		prev = next
	}
}

func TestNowSurvivesClockGoingBackwards(t *testing.T) {
	c := NewClock("node-a", Timestamp{})
	c.SetNow(fixed(5000))
	before := c.Now()
	c.SetNow(fixed(1000)) // e.g. NTP correction
	after := c.Now()
	if Compare(after, before) <= 0 {
		t.Fatalf("timestamp went backwards: %s then %s", before, after)
	}
}

func TestNewClockStartsAfterPersistedTimestamp(t *testing.T) {
	persisted := Timestamp{Wall: 9000, Counter: 4, Node: "node-a"}
	c := NewClock("node-a", persisted)
	c.SetNow(fixed(1000))
	if got := c.Now(); Compare(got, persisted) <= 0 {
		t.Fatalf("got %s, want after %s", got, persisted)
	}
}

func TestObserveOrdersLocalEventsAfterRemote(t *testing.T) {
	// Device B's clock is far behind device A's. After B observes A's edit,
	// B's next edit must still win, because it causally happened later.
	a := NewClock("node-a", Timestamp{})
	a.SetNow(fixed(100_000))
	remote := a.Now()

	b := NewClock("node-b", Timestamp{})
	b.SetNow(fixed(1_000))
	b.Observe(remote)
	local := b.Now()
	if Compare(local, remote) <= 0 {
		t.Fatalf("local %s should sort after observed remote %s", local, remote)
	}
}

func TestStringOrderMatchesLogicalOrder(t *testing.T) {
	stamps := []Timestamp{
		{Wall: 0x10, Counter: 0, Node: "b"},
		{Wall: 0x9, Counter: 300, Node: "z"},
		{Wall: 0x10, Counter: 1, Node: "a"},
		{Wall: 0x10, Counter: 0, Node: "a"},
		{Wall: 1 << 40, Counter: 0, Node: "a"},
	}
	strs := make([]string, len(stamps))
	for i, s := range stamps {
		strs[i] = s.String()
	}
	sort.Strings(strs)
	want := []string{
		Timestamp{Wall: 0x9, Counter: 300, Node: "z"}.String(),
		Timestamp{Wall: 0x10, Counter: 0, Node: "a"}.String(),
		Timestamp{Wall: 0x10, Counter: 0, Node: "b"}.String(),
		Timestamp{Wall: 0x10, Counter: 1, Node: "a"}.String(),
		Timestamp{Wall: 1 << 40, Counter: 0, Node: "a"}.String(),
	}
	for i := range want {
		if strs[i] != want[i] {
			t.Fatalf("position %d: got %s, want %s", i, strs[i], want[i])
		}
	}
}

func TestParseRoundTrip(t *testing.T) {
	in := Timestamp{Wall: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC).UnixMilli(), Counter: 42, Node: "6f1c-node"}
	out, err := Parse(in.String())
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("got %+v, want %+v", out, in)
	}
	for _, bad := range []string{"", "abc", "0000000000000001.00000001.", "zz00000000000001.00000001.n"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}
