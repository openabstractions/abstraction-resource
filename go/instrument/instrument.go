// Package instrument measures who holds a scarce resource on this machine.
//
// It is the half of abstraction.resource/table@1 that reads the platform. The
// table service turns a Reading into rows with evidence verified; a claim from
// a host adapter never passes through here. The code moved out of
// abstraction-router on 2026-09-22: the router measured the card because
// nothing else did, and the measurement in
// research/resources/MEASUREMENT-2026-09-22.md is what the table is built on.
package instrument

import (
	"strings"
	"sync"
	"time"
)

// The instrument names CONTRACT.md RES-T2 allows in ResourceState.instrument.
const (
	NameWindowsGPUCounters = "windows-gpu-counters"
	NameLinuxFdinfo        = "linux-fdinfo"
	// NameMetalWorkingSet measures card:0's capacity on a Mac and no
	// holder: every row it reports is claimed.
	NameMetalWorkingSet = "metal-working-set"
	NameNone            = "none"
	// NameFixed is the instrument whose readings its owner sets: tests and
	// the conformance driver, never a running service.
	NameFixed = "fixed"
)

// Card0 is the first accelerator's memory. On an APU it and memory report the
// same bytes; a reader never sums them (CONTRACT.md).
const Card0 = "card:0"

// Threshold is the smallest hold reported as a row. Below it every desktop
// process on a shared-memory machine is a holder, and a table of ninety rows
// answers nobody's question about who holds the card.
const Threshold = 256 << 20

// A Sample is one process's hold, as the platform reports it.
//
// Program is the absolute image path read through a process handle, which is
// the same evidence identity.SubjectProgram takes for a bound caller at
// ProofBound. Account is the process token's owner: a Windows SID or a POSIX
// uid. Either is empty when the platform would not say, and nothing fills it
// in: Image then carries the process image name the counter set gives, and the
// row keeps it instead of a program (CONTRACT.md RES-T1).
//
// Detail carries a figure Amount is not: on Linux, an APU's `drm-resident-gtt`
// beside the `drm-resident-vram` that Amount already is, because a carve-out
// VRAM pool and its GTT window are not the same bytes and RES-T5 forbids
// summing them into one number. It is empty wherever there is nothing to add.
type Sample struct {
	PID int
	// StartID is the platform's exact process creation identity (Windows
	// creation FILETIME or Linux boot-relative start ticks). Empty means a
	// lease cannot credit this sample to a child process.
	StartID string
	Program string
	Account string
	Image   string
	Amount  int64
	Detail  string
}

// ProcessInfo is the kernel-observed identity needed to associate a process
// sample with one lease. It is internal accounting evidence, not a table row.
type ProcessInfo struct {
	PID        int
	ParentPID  int
	StartID    string
	BootID     string
	StartTicks uint64
	Started    time.Time
	Account    string
	// Image is the process's full image path where the platform reads it
	// through the same handle (Windows); empty elsewhere. The lease book
	// uses it to keep an engine's own interpreter inside the engine's folder.
	Image string
}

// A Reading is one observation of one resource.
//
// Capacity is bytes and 0 when the instrument cannot say, which is the case on
// a shared-memory adapter whose pool is the machine's own RAM.
type Reading struct {
	Resource string
	Capacity int64
	Samples  []Sample
	At       time.Time
}

// An Instrument measures holds of a resource on this machine.
//
// Read returns what it saw now; caching and age bounds belong to the table
// service, not here. A resource this instrument does not report is not an
// error: it returns a Reading with no samples, and the table's rows for it are
// whatever the claims supply.
type Instrument interface {
	// Name is what ResourceState.instrument carries.
	Name() string
	// Resources are the resources this instrument can measure, in order.
	Resources() []string
	Read(resource string) (Reading, error)
}

// ProcessSampleIdentity is implemented by an instrument whose process samples
// carry a creation identity established while that process was pinned for
// the measurement. A lease may credit child use only with this evidence.
type ProcessSampleIdentity interface {
	VerifiedProcessSamples() bool
}

// None is the instrument of a machine where nothing measures the resource. Its
// readings are empty, so every row the table reports is claimed.
func None() Instrument { return none{} }

type none struct{}

func (none) Name() string                 { return NameNone }
func (none) Resources() []string          { return nil }
func (none) VerifiedProcessSamples() bool { return false }

// PoolOf reads a card as carved from memory, as the Windows and Linux
// instruments do: with nothing measuring, a card's bytes are the machine's
// memory (unified on Apple Silicon; a discrete card is the limit CONTRACT.md
// RES-L2 states).
func (none) PoolOf(resource string) string { return sharedWithMemory(resource) }
func (none) Read(resource string) (Reading, error) {
	return Reading{Resource: resource, At: time.Now()}, nil
}

// Memory is the machine's system memory as a resource name.
const Memory = "memory"

// CardPrefix begins every accelerator resource, card:0 first.
const CardPrefix = "card:"

// A Pooled instrument says which resource's bytes another resource is carved
// from. A shared-memory adapter's card:<n> is carved from memory, so a lease
// on one and a lease on the other spend the same bytes and are admitted
// against one pool (CONTRACT.md RES-L2). PoolOf returns the resource whose
// pool this resource spends, or "" when the resource is its own pool.
type Pooled interface {
	PoolOf(resource string) string
}

// PoolOf is the resource whose pool resource spends under in: the one its
// Pooled declaration names, and resource itself otherwise.
func PoolOf(in Instrument, resource string) string {
	if pooled, ok := in.(Pooled); ok {
		if pool := pooled.PoolOf(resource); pool != "" {
			return pool
		}
	}
	return resource
}

// sharedWithMemory is the declaration of an instrument that cannot tell a
// discrete adapter from a shared-memory one: every card:<n> is read as carved
// from memory. On a discrete card this admits against system memory, which is
// the limit CONTRACT.md RES-L2 states until an instrument reads the adapter's
// dedicated capacity.
func sharedWithMemory(resource string) string {
	if strings.HasPrefix(resource, CardPrefix) {
		return Memory
	}
	return ""
}

// FixedResource is one resource a Fixed instrument reports.
type FixedResource struct {
	// Capacity is the pool's bytes; 0 says the instrument cannot tell.
	Capacity int64
	// Pool names the resource whose bytes this one is carved from, "" for
	// its own pool.
	Pool    string
	Samples []Sample
}

// Fixed is an instrument whose readings its owner sets. The service's tests
// and the conformance driver measure through it (dmap-1 I-10): a 24 GiB
// discrete card or a 128 GiB shared-memory pool is a value here, never a GPU
// run. Fixed is safe for concurrent use.
type Fixed struct {
	mu        sync.Mutex
	resources map[string]FixedResource
	order     []string
}

// NewFixed returns a Fixed instrument reporting nothing yet.
func NewFixed() *Fixed { return &Fixed{resources: map[string]FixedResource{}} }

// Set replaces what the instrument reports for resource.
func (f *Fixed) Set(resource string, r FixedResource) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, seen := f.resources[resource]; !seen {
		f.order = append(f.order, resource)
	}
	r.Samples = append([]Sample(nil), r.Samples...)
	f.resources[resource] = r
}

// Name is NameFixed.
func (f *Fixed) Name() string { return NameFixed }

// Resources are the resources set so far, in the order they were first set.
func (f *Fixed) Resources() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.order...)
}

// PoolOf is the Pool each resource was set with.
func (f *Fixed) PoolOf(resource string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resources[resource].Pool
}

// VerifiedProcessSamples is false: a set sample pinned no process.
func (f *Fixed) VerifiedProcessSamples() bool { return false }

// Read returns what was set for resource, and an empty reading otherwise.
func (f *Fixed) Read(resource string) (Reading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.resources[resource]
	return Reading{Resource: resource, Capacity: r.Capacity,
		Samples: append([]Sample(nil), r.Samples...), At: time.Now()}, nil
}
