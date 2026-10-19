package service

import (
	"sort"
	"strconv"
	"sync"
	"time"
)

// The resource book owns the platform request. Compatibility hosts release
// connection-owned leases when their authenticated connection ends.

// An AwakeHold is one live wake hold as the book records it.
type AwakeHold struct {
	Lease   string
	Program string
	Account string
	Why     string
	Since   time.Time
}

// HoldAwake writes one connection-owned lease of resource awake and returns
// its id and the release that ends it. The caller has already decided
// ActionHold on ResourceAwake for this holder: this is the record, not the
// gate, and a host that calls it without deciding has granted a hold nobody
// permitted.
//
// The lease never expires on a clock. It ends when release is called, which
// the caller does when the holder's connection goes.
func (b *Book) HoldAwake(who Subject, why string) (string, func()) {
	id, release, _ := b.holdAwake(who, why, true)
	return id, release
}

// HoldAwakeChecked bridges a pre-authorized connection hold using its original
// subject. The resource book owns its system request; release ends the lease.
func (b *Book) HoldAwakeChecked(who Subject, why string) (string, func(), error) {
	return b.holdAwake(who, why, false)
}

func (b *Book) holdAwake(who Subject, why string, recordOnly bool) (string, func(), error) {
	b.mu.Lock()
	b.seq++
	now := b.now()
	live := &record{ID: "awake-" + strconv.FormatUint(b.seq, 10), Resource: ResourceAwake,
		Program: who.Program, Account: who.Account, Grant: AwakeRight, Detail: oneLine(why),
		Since: now, RenewBy: now, held: true, recordOnly: recordOnly}
	b.leases[live.ID] = live
	b.syncAwake()
	if !recordOnly && b.keepSystemAwake != nil && b.systemAwake == nil {
		delete(b.leases, live.ID)
		err := b.systemError
		b.mu.Unlock()
		return "", func() {}, err
	}
	b.mu.Unlock()
	b.record(auditRecord{Event: "acquired", Asker: who.Program, Lease: live.ID,
		Resource: ResourceAwake, Answer: "acquired"})
	var once sync.Once
	return live.ID, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.leases, live.ID)
			b.syncAwake()
			b.mu.Unlock()
			b.record(auditRecord{Event: "released", Asker: who.Program, Lease: live.ID,
				Resource: ResourceAwake, Answer: "released"})
		})
	}, nil
}

// AwakeHolds is who holds the machine awake now, oldest first. It is the one
// answer every reader of the wake hold reads, the command line and the Panel
// alike, because it is the table's awake rows.
func (b *Book) AwakeHolds() []AwakeHold {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []AwakeHold{}
	for _, live := range b.leases {
		if live.Resource != ResourceAwake {
			continue
		}
		out = append(out, AwakeHold{Lease: live.ID, Program: live.Program, Account: live.Account,
			Why: live.Detail, Since: live.Since})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Since.Equal(out[j].Since) {
			return out[i].Since.Before(out[j].Since)
		}
		return out[i].Lease < out[j].Lease
	})
	return out
}
