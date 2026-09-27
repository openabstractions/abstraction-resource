// Package instrument measures who holds a scarce resource on this machine.
//
// It is the half of abstraction.resource/table@1 that reads the platform. The
// table service turns a Reading into rows with evidence verified; a claim from
// a host adapter never passes through here. The code moved out of
// abstraction-router on 2026-09-22: the router measured the card because
// nothing else did, and the measurement in
// research/resources/MEASUREMENT-2026-09-22.md is what the table is built on.
package instrument

import "time"

// The instrument names CONTRACT.md RES-T2 allows in ResourceState.instrument.
const (
	NameWindowsGPUCounters = "windows-gpu-counters"
	NameLinuxFdinfo        = "linux-fdinfo"
	NameNone               = "none"
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
func (none) Read(resource string) (Reading, error) {
	return Reading{Resource: resource, At: time.Now()}, nil
}
