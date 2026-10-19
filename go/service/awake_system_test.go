package service

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

func TestNonCardWakeSystemLifetimeAndRevocation(t *testing.T) {
	now := time.Now()
	system, display := &platformWake{}, &platformWake{}
	permit := true
	book := cardBook(t, card(120*gib), BookOptions{Now: func() time.Time { return now },
		KeepSystemAwake: system.keep, KeepAwake: display.keep,
		Standing: func(context.Context, Subject, string, string) error {
			if !permit {
				return errors.New("revoked")
			}
			return nil
		}})
	who := Subject{Program: `C:\download.exe`, Account: "account"}
	take := func() string {
		result := book.Acquire(context.Background(), who, &identity.Peer{}, ResourceAwake, 0, 0)
		if result.Outcome != wire.AcquireOutcomeAcquired {
			t.Fatal(result.Outcome)
		}
		return result.Lease.ID
	}
	first, second := take(), take()
	if taken, released := system.counts(); taken != 1 || released != 0 {
		t.Fatal(taken, released)
	}
	if taken, _ := display.counts(); taken != 0 {
		t.Fatal("non-card wake required display")
	}
	book.Release(who, first)
	if _, released := system.counts(); released != 0 {
		t.Fatal("first release ended shared request")
	}
	book.Release(who, second)
	if _, released := system.counts(); released != 1 {
		t.Fatal("last release retained system request")
	}
	take()
	permit = false
	book.Recheck(context.Background())
	if taken, released := system.counts(); taken != 2 || released != 2 {
		t.Fatal("revoke", taken, released)
	}
	permit = true
	take()
	now = now.Add(2 * DefaultLifetime)
	book.Expire()
	if taken, released := system.counts(); taken != 3 || released != 3 {
		t.Fatal("expiry", taken, released)
	}
}

func TestCardWakeKeepsDisplayAndReturnsToSystem(t *testing.T) {
	system, display := &platformWake{}, &platformWake{}
	book := cardBook(t, card(120*gib), BookOptions{KeepSystemAwake: system.keep, KeepAwake: display.keep})
	who := Subject{Program: `C:\render.exe`, Account: "account"}
	book.Acquire(context.Background(), who, &identity.Peer{}, ResourceAwake, 0, 0)
	card := book.Acquire(context.Background(), who, &identity.Peer{}, instrument.Card0, gib, 0)
	if card.Outcome != wire.AcquireOutcomeAcquired {
		t.Fatal(card.Outcome)
	}
	if taken, released := display.counts(); taken != 1 || released != 0 {
		t.Fatal("display pair", taken, released)
	}
	if taken, released := system.counts(); taken != 1 || released != 1 {
		t.Fatal("system to display", taken, released)
	}
	book.Release(who, card.Lease.ID)
	if taken, released := display.counts(); taken != 1 || released != 1 {
		t.Fatal("card ended", taken, released)
	}
	if taken, released := system.counts(); taken != 2 || released != 1 {
		t.Fatal("display to system", taken, released)
	}
}

func TestLegacyConnectionWakeBookKeepsOriginalSubject(t *testing.T) {
	system := &platformWake{}
	book := cardBook(t, card(120*gib), BookOptions{KeepSystemAwake: system.keep})
	who := Subject{Program: `C:\caller.exe`, Account: "original-account"}
	id, release, err := book.HoldAwakeChecked(who, "download")
	if err != nil {
		t.Fatal(err)
	}
	rows := book.AwakeHolds()
	if len(rows) != 1 || rows[0].Program != who.Program || rows[0].Account != who.Account || rows[0].Lease != id {
		t.Fatal(rows)
	}
	book.Expire()
	if _, released := system.counts(); released != 0 {
		t.Fatal("connection hold expired")
	}
	release()
	release()
	if taken, released := system.counts(); taken != 1 || released != 1 {
		t.Fatal(taken, released)
	}
	if len(book.AwakeHolds()) != 0 {
		t.Fatal("connection cleanup retained lease")
	}
}

func TestLegacyConnectionWakePreservesPlatformRefusal(t *testing.T) {
	refusal := errors.New("platform denied the request")
	book := cardBook(t, card(120*gib), BookOptions{KeepSystemAwake: func(string) (func(), error) { return nil, refusal }})
	id, _, err := book.HoldAwakeChecked(Subject{Program: `C:\caller.exe`}, "download")
	if id != "" || !errors.Is(err, refusal) || len(book.AwakeHolds()) != 0 {
		t.Fatal(id, err, book.AwakeHolds())
	}
}
