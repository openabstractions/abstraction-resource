//go:build !windows

package instrument

import (
	"errors"
	"runtime"
)

// AdapterUnified reports whether card:0 draws its memory from the machine's
// RAM. Apple Silicon always does. Elsewhere outside Windows no reading is
// built, and the answer is unknown.
func AdapterUnified() (bool, error) {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return true, nil
	}
	return false, errors.New("whether the graphics adapter shares system memory is unknown here")
}
