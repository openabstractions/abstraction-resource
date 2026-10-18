package instrument

import (
	"errors"
	"sort"
)

// GraphicsAdapters names the graphics adapters the platform reports now: the
// instances of `\GPU Adapter Memory(*)\Dedicated Usage`, one per adapter LUID
// (`luid_0x..._phys_0`). GPU fault handling reads it after a driver reset: an
// adapter the counters list again is the first half of the probe that lets
// engines reload (CONTRACT.md RES-G4). An empty list is an error, because a
// machine with a GPU that lists none is a machine whose driver is not back.
func GraphicsAdapters() ([]string, error) {
	rows, err := counters(`\GPU Adapter Memory(*)\Dedicated Usage`)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, row := range rows {
		if row.instance == "" || seen[row.instance] {
			continue
		}
		seen[row.instance] = true
		names = append(names, row.instance)
	}
	if len(names) == 0 {
		return nil, errors.New("the GPU adapter counters list no adapter")
	}
	sort.Strings(names)
	return names, nil
}
