package instrument

import (
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
