//go:build !windows

package instrument

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
)

// GraphicsAdapters names the graphics adapters the platform reports now. On Linux it
// is the DRM cards under /sys/class/drm (card0, card1, never their
// connectors); elsewhere there is no listing and the answer is
// errors.ErrUnsupported (CONTRACT.md RES-G4).
func GraphicsAdapters() ([]string, error) {
	matches, err := filepath.Glob("/sys/class/drm/card[0-9]*")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, path := range matches {
		name := filepath.Base(path)
		if strings.Contains(name, "-") {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, errors.ErrUnsupported
	}
	sort.Strings(names)
	return names, nil
}
