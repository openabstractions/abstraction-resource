package instrument

import (
	"math"
	"os"
	"strconv"
	"testing"
)

func heldBytes(r Reading) float64 {
	var t float64
	for _, s := range r.Samples {
		t += float64(s.Amount)
	}
	return t / (1 << 30)
}

// The first reading in a process used to be a different number from the second
// — 2.81 GiB against 24.01 for the process holding this machine's model — and
// a table is worth nothing if its first reading is wrong. This also proves the
// attribution the table depends on: a process holding gigabytes of the card is
// named, by an absolute image path where the handle opens and by its process
// image name where it does not.
func TestTheFirstReadingIsTheSecond(t *testing.T) {
	if os.Getenv("OA_LIVE_RESOURCES") != "1" {
		t.Skip("set OA_LIVE_RESOURCES=1: this reads the whole machine")
	}
	in := Detect()
	if in.Name() == NameNone {
		t.Skip("no instrument on this platform")
	}
	first, err := in.Read(Card0)
	if err != nil {
		t.Skip(err)
	}
	second, err := in.Read(Card0)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(heldBytes(first)-heldBytes(second)) > 1 {
		t.Fatalf("%.2f GiB then %.2f GiB: %v vs %v", heldBytes(first), heldBytes(second), first, second)
	}
	for _, s := range first.Samples {
		if s.Program == "" && s.Image == "pid"+strconv.Itoa(s.PID) {
			t.Fatalf("a process holding %.2f GiB is unnamed: %+v", float64(s.Amount)/(1<<30), s)
		}
		name := s.Program
		if name == "" {
			name = s.Image
		}
		t.Logf("%-56s account=%-14s pid %-7d %6.2f GiB", name, s.Account, s.PID, float64(s.Amount)/(1<<30))
	}
	t.Logf("%.2f GiB held by %d processes", heldBytes(first), len(first.Samples))
}
