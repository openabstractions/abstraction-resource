package instrument

import (
	"fmt"
	"strconv"
	"strings"
)

// The part of the Windows counter instrument that is arithmetic rather than
// platform. It lives outside counters_windows.go so every platform compiles it
// and its tests: the file suffix once hid this half from two of three
// platforms and the tests that called it stopped building there.

// A counter pairs a PDH instance name with the value read for it. It stays a
// pair rather than a map keyed by instance name: two instances can carry one
// name, and summing them turned a process id into a number no process has,
// which left the process holding 46 GB unnamed.
type counter struct {
	instance string
	value    int64
}

// residentByPID sums `\GPU Process Memory(*)\Total Committed` counters per
// process id, parsed out of the PDH instance name (`pid_<n>_...`). A pid can
// own more than one instance of this counter, one per adapter LUID it touches,
// and summing those is correct: each LUID is a different adapter, not an
// overlapping share of the same bytes. See counters_windows.go's header and
// research/resources/MEASUREMENT-2026-09-22.md for why the instrument reads
// this one counter instead of summing Local Usage and Dedicated Usage, which
// are not different adapters but different names for the same bytes.
func residentByPID(mem []counter) map[int]int64 {
	total := map[int]int64{}
	for _, c := range mem {
		_, rest, ok := strings.Cut(c.instance, "pid_")
		if !ok {
			continue
		}
		digits, _, _ := strings.Cut(rest, "_")
		if pid, err := strconv.Atoi(digits); err == nil {
			total[pid] += c.value
		}
	}
	return total
}

// adapterInstance is the token PDH's GPU instance names carry for one adapter
// LUID: `luid_0x<high>_0x<low>`, eight hex digits each. A LUID is fixed for
// one boot only.
func adapterInstance(high int32, low uint32) string {
	return fmt.Sprintf("luid_0x%08x_0x%08x", uint32(high), low)
}

// onAdapter keeps the counter rows of one adapter, named by its
// adapterInstance token; PDH writes the hex digits in either case. An empty
// adapter keeps every row.
func onAdapter(mem []counter, adapter string) []counter {
	if adapter == "" {
		return mem
	}
	var out []counter
	for _, c := range mem {
		name := strings.ToLower(c.instance)
		i := strings.Index(name, adapter)
		if i < 0 {
			continue
		}
		if end := i + len(adapter); end == len(name) || name[end] == '_' {
			out = append(out, c)
		}
	}
	return out
}
