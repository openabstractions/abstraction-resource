package service

import (
	"path/filepath"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

func wakeBook(t *testing.T, path string, now func() time.Time) (*Book, *Table) {
	t.Helper()
	table := NewTable(&fake{name: instrument.NameNone, at: time.Now()}, nil, time.Nanosecond)
	book, err := OpenBook(BookOptions{Table: table, Path: path, Lifetime: time.Second, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	table.claims = book.Claims()
	return book, table
}

// A wake hold is a row of the table's awake resource, under the rule that
// grants it, and it is the same row every reader reads (CONTRACT.md RES-A1).
func TestAWakeHoldIsARowOfTheAwakeResource(t *testing.T) {
	book, table := wakeBook(t, "", time.Now)
	who := Subject{Program: `C:\downloader.exe`, Account: "S-1-5-21-7-1001"}
	lease, release := book.HoldAwake(who, "a six-hour download")
	if lease == "" {
		t.Fatal("the wake hold has no lease")
	}
	state, err := table.Holders(ResourceAwake, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 1 {
		t.Fatalf("awake rows %+v", state.Holders)
	}
	row := state.Holders[0]
	if row.Program != who.Program || row.Lease != lease || row.Grant != AwakeRight || row.Detail != "a six-hour download" {
		t.Fatalf("the awake row is %+v", row)
	}
	if row.Evidence != wire.EvidenceClaimed || state.Held != 0 {
		t.Fatalf("a wake hold was counted as measured bytes: %+v held %d", row, state.Held)
	}
	holds := book.AwakeHolds()
	if len(holds) != 1 || holds[0].Lease != lease || holds[0].Why != "a six-hour download" {
		t.Fatalf("AwakeHolds %+v does not match the table's row", holds)
	}
	release()
	release() // releasing twice is releasing once
	if state, _ = table.Holders(ResourceAwake, true); len(state.Holders) != 0 {
		t.Fatalf("a released wake hold is still a row: %+v", state.Holders)
	}
	if len(book.AwakeHolds()) != 0 {
		t.Fatal("a released wake hold is still held")
	}
}

// The wake hold's lifetime is its holder's connection, not a clock: it never
// expires on renew_by and it never survives a restart, because the connection
// that owned it did not either.
func TestAWakeHoldLivesByItsHolderAndNotByTheClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.json")
	clock := time.Now()
	book, table := wakeBook(t, path, func() time.Time { return clock })
	who := Subject{Program: `C:\downloader.exe`, Account: "S-1-5-21-7-1001"}
	lease, _ := book.HoldAwake(who, "held open")

	clock = clock.Add(time.Hour)
	book.Expire()
	state, err := table.Holders(ResourceAwake, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 1 || state.Holders[0].Lease != lease {
		t.Fatalf("an hour of clock ended a hold whose holder is still connected: %+v", state.Holders)
	}

	restarted, _ := wakeBook(t, path, func() time.Time { return clock })
	if len(restarted.AwakeHolds()) != 0 {
		t.Fatalf("a restart revived a hold whose connection is gone: %+v", restarted.AwakeHolds())
	}
}
