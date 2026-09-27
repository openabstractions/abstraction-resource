// Package service is abstraction.resource/table@1 over an instrument and the
// claims the machine's host adapters make.
package service

import (
	"sort"
	"sync"
	"time"

	wire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// DefaultAge is how long a sample stands for a reader that did not ask for a
// fresh one. The unload measurement in
// research/resources/MEASUREMENT-2026-09-22.md saw the counters agree with LM
// Studio's reply within one 300 ms poll, so a two-second table is never more
// than a moment behind what a person is watching, and a window that repaints
// on a clock does not re-read the whole counter set every frame.
const DefaultAge = 2 * time.Second

// A Claim is a row no instrument measured. A host adapter's own model list is
// the engine's claim that the model is resident; it carries no amount, because
// the hosts measured in
// research/resources/MEASUREMENT-2026-09-22.md report none (CONTRACT.md
// RES-T3).
//
// Program names the claimant as rights names a subject where there is one: an
// absolute image path or a package family. An attached HTTP engine has no
// proven image, and its rows name it host:<name> with an empty account.
// Lease and Amount are set only by the lease book, which supplies its live
// leases as claims so that one reader of the table sees grants and holds
// together (CONTRACT.md RES-T3, RES-A1). A lease's amount is the grant the
// service itself wrote down, not an adapter's estimate of what is resident;
// held still sums the verified rows alone, so a granted lease is never added
// to the bytes an instrument measured.
type Claim struct {
	Resource string
	Program  string
	Account  string
	Grant    string
	Detail   string
	Since    time.Time
	Lease    string
	Amount   int64
}

// Claims supplies the claimed rows of this machine at the moment it is called.
// It must not call back into the table.
type Claims func() []Claim

// JoinClaims reads each source in turn. Nil sources are skipped, so a
// composition can name a source it has not built yet.
func JoinClaims(sources ...Claims) Claims {
	return func() []Claim {
		var out []Claim
		for _, source := range sources {
			if source != nil {
				out = append(out, source()...)
			}
		}
		return out
	}
}

// A Table answers abstraction.resource/table@1 from one instrument and one
// source of claims. It holds no resource and changes nothing.
type Table struct {
	instrument instrument.Instrument
	claims     Claims
	age        time.Duration
	now        func() time.Time
	refresh    func()

	mu     sync.Mutex
	cached map[string]instrument.Reading
}

// NewTable builds the table. A nil instrument is instrument.None, nil claims
// are no claims, and a non-positive age is DefaultAge.
func NewTable(in instrument.Instrument, claims Claims, age time.Duration) *Table {
	if in == nil {
		in = instrument.None()
	}
	if age <= 0 {
		age = DefaultAge
	}
	return &Table{instrument: in, claims: claims, age: age, now: time.Now, cached: map[string]instrument.Reading{}}
}

// SetRefresh installs what a fresh Holders calls before it rereads claims. A
// claims source whose evidence is a host's own last survey (hostClaims in
// serve/runtime_resources.go) does not itself provoke a new one, because a
// claims callback that surveyed would loop against a residency reader that
// asks the table; refresh is the table's own way of asking for a new survey
// without that loop (CONTRACT.md RES-T3). A table with no hook rereads its
// claims exactly as it already did: JoinClaims's sources are called on every
// Holders, refresh or not.
func (t *Table) SetRefresh(refresh func()) { t.refresh = refresh }

// Instrument names what measures for this table.
func (t *Table) Instrument() string { return t.instrument.Name() }

func (t *Table) verifiedProcessSamples() bool {
	proof, ok := t.instrument.(instrument.ProcessSampleIdentity)
	return ok && proof.VerifiedProcessSamples()
}

// Resources are the resources this machine's instrument and adapters can
// report, in order and without repetition.
func (t *Table) Resources() (wire.ResourceList, error) {
	seen := map[string]bool{}
	out := wire.ResourceList{Resources: []string{}, Observed: stamp(t.now())}
	for _, r := range t.instrument.Resources() {
		if r != "" && !seen[r] {
			seen[r], out.Resources = true, append(out.Resources, r)
		}
	}
	for _, c := range t.claimed() {
		if c.Resource != "" && !seen[c.Resource] {
			seen[c.Resource], out.Resources = true, append(out.Resources, c.Resource)
		}
	}
	return out, nil
}

// Holders is who holds resource now. fresh reads the instrument again and, if
// the table was given a refresh hook, asks it for a new survey first, so a
// claimed host: row a survey would no longer report is gone from a fresh read
// too and not just from the verified rows (CONTRACT.md RES-T3). A caller that
// did not ask for fresh reads the instrument's last sample while it is inside
// the age bound; claims are read on every call regardless, which costs a map
// read of whatever the runtime last surveyed.
func (t *Table) Holders(resource string, fresh bool) (wire.ResourceState, error) {
	state, _, err := t.snapshot(resource, fresh)
	return state, err
}

// snapshot gives the lease book the same instrument samples as the public
// rows it reports. Process identities stay inside the service boundary.
func (t *Table) snapshot(resource string, fresh bool) (wire.ResourceState, []instrument.Sample, error) {
	if fresh && t.refresh != nil {
		t.refresh()
	}
	reading, err := t.read(resource, fresh)
	if err != nil {
		return wire.ResourceState{}, nil, err
	}
	state := wire.ResourceState{Resource: resource, Capacity: reading.Capacity, Holders: []wire.Holder{},
		Observed: stamp(reading.At), Instrument: t.instrument.Name()}
	for _, s := range reading.Samples {
		program := s.Program
		if program == "" {
			program = s.Image
		}
		state.Holders = append(state.Holders, wire.Holder{Program: program, Account: s.Account,
			Amount: s.Amount, Evidence: wire.EvidenceVerified, Detail: s.Detail})
		state.Held += s.Amount
	}
	for _, c := range t.claimed() {
		if c.Resource != resource {
			continue
		}
		row := wire.Holder{Program: c.Program, Account: c.Account, Evidence: wire.EvidenceClaimed,
			Grant: c.Grant, Detail: c.Detail, Lease: c.Lease, Amount: c.Amount}
		if !c.Since.IsZero() {
			row.Since = stamp(c.Since)
		}
		state.Holders = append(state.Holders, row)
	}
	return state, reading.Samples, nil
}

// read returns the reading Holders reports: the cached one while it is inside
// the age bound and fresh was not asked for, and a new one otherwise.
func (t *Table) read(resource string, fresh bool) (instrument.Reading, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !fresh {
		if cached, ok := t.cached[resource]; ok && t.now().Sub(cached.At) < t.age {
			return cached, nil
		}
	}
	reading, err := t.instrument.Read(resource)
	if err != nil {
		return instrument.Reading{}, err
	}
	if reading.At.IsZero() {
		reading.At = t.now()
	}
	reading.Resource = resource
	t.cached[resource] = reading
	return reading, nil
}

// claimed is the claims of this machine, sorted so two readings of an
// unchanged machine are the same table.
func (t *Table) claimed() []Claim {
	if t.claims == nil {
		return nil
	}
	out := t.claims()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Program != out[j].Program {
			return out[i].Program < out[j].Program
		}
		return out[i].Detail < out[j].Detail
	})
	return out
}

// stamp writes the contract's timestamp: rfc3339 with microseconds, in UTC.
func stamp(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000000Z") }
