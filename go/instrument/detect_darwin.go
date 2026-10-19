//go:build darwin

package instrument

// Detect is the instrument of this Mac: the Metal working-set capacity
// instrument where the system lists a Metal device, and None otherwise.
func Detect() Instrument {
	if metalWorkingSet() <= 0 {
		return None()
	}
	return metalInstrument{workingSet: metalWorkingSet}
}
