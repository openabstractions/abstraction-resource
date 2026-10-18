package gpufault

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// stateFile is the quarantine as it is kept across restarts. A driver reset
// can end the provider with its engines, and a quarantine that a restart
// forgets is a crash loop with extra steps (RES-G3).
type stateFile struct {
	Version int               `json:"version"`
	Entries []QuarantineEntry `json:"entries"`
}

const stateVersion = 1

// ErrDamagedQuarantine is a quarantine file that exists and cannot be used:
// not JSON, another version, or an entry whose key is not its fingerprint's.
var ErrDamagedQuarantine = errors.New("the quarantine file is damaged")

func loadState(path string) ([]QuarantineEntry, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gpufault: reading the quarantine %s: %w", path, err)
	}
	var state stateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("gpufault: %w: %s is not JSON: %v", ErrDamagedQuarantine, path, err)
	}
	if state.Version != stateVersion {
		return nil, fmt.Errorf("gpufault: %w: %s has version %d, not %d", ErrDamagedQuarantine, path, state.Version, stateVersion)
	}
	for _, e := range state.Entries {
		if e.Key == "" || e.Key != e.Fingerprint.Key() {
			return nil, fmt.Errorf("gpufault: %w: %s has an entry whose key does not match its fingerprint", ErrDamagedQuarantine, path)
		}
	}
	return state.Entries, nil
}

// moveAside renames a damaged quarantine file to path.bad-<UTC time> in the
// same folder, kept for a person to read, and returns the new name.
func moveAside(path string, now time.Time) (string, error) {
	aside := path + ".bad-" + now.UTC().Format("20060102T150405.000000000Z")
	if err := os.Rename(path, aside); err != nil {
		return "", err
	}
	return aside, nil
}

// openState is loadState that moves a damaged file aside and starts empty:
// a quarantine a provider cannot read must neither stop the provider nor be
// lost (RES-G3). moved is where a damaged file went; why is its damage.
func openState(path string, now time.Time) (entries []QuarantineEntry, moved string, why error, err error) {
	entries, err = loadState(path)
	if err == nil || !errors.Is(err, ErrDamagedQuarantine) {
		return entries, "", nil, err
	}
	moved, moveErr := moveAside(path, now)
	if moveErr != nil {
		return nil, "", err, fmt.Errorf("gpufault: moving the damaged quarantine %s aside: %w", path, moveErr)
	}
	return nil, moved, err, nil
}

// saveLocked writes the quarantine through a temporary file and a rename, so
// a stop mid-write leaves the previous file whole; g.mu is held.
func (g *Guard) saveLocked() error {
	path := g.opts.State
	if path == "" {
		return nil
	}
	state := stateFile{Version: stateVersion, Entries: make([]QuarantineEntry, 0, len(g.quarantine))}
	for _, e := range g.quarantine {
		state.Entries = append(state.Entries, *e)
	}
	sort.Slice(state.Entries, func(i, j int) bool { return state.Entries[i].Key < state.Entries[j].Key })
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	if _, err := temp.Write(append(data, '\n')); err != nil {
		//unchecked: the write failure is the error returned; the partial file is removed below
		temp.Close()
		//unchecked: removing the partial temporary file is best effort
		os.Remove(name)
		return err
	}
	if err := temp.Close(); err != nil {
		//unchecked: removing the partial temporary file is best effort
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		//unchecked: removing the unrenamed temporary file is best effort
		os.Remove(name)
		return err
	}
	g.stateSeen = stateStamp(path)
	return nil
}

// stamp is a state file's modification time and size, enough to see that
// another process (modelhostd gpu clear) rewrote it.
type stamp struct {
	mod  int64
	size int64
}

func stateStamp(path string) stamp {
	info, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{mod: info.ModTime().UnixNano(), size: info.Size()}
}

// refreshLocked rereads the state file when another process changed it, so
// a person's clear reaches the running provider at its next check; g.mu is
// held. A file that cannot be read keeps the quarantine in memory.
func (g *Guard) refreshLocked() {
	if g.opts.State == "" {
		return
	}
	now := stateStamp(g.opts.State)
	if now == g.stateSeen {
		return
	}
	// The change is seen once, whatever it holds, so a file that cannot be
	// read is reported once rather than at every check.
	g.stateSeen = now
	entries, err := loadState(g.opts.State)
	if err != nil {
		g.report(err)
		return
	}
	g.quarantine = map[string]*QuarantineEntry{}
	for i := range entries {
		e := entries[i]
		g.quarantine[e.Key] = &e
	}
}

// ReadQuarantine is the quarantine kept at path. A damaged file reads an
// error wrapping ErrDamagedQuarantine and stays where it is.
func ReadQuarantine(path string) ([]QuarantineEntry, error) { return loadState(path) }

// DamagedQuarantines are the damaged quarantine files moved aside beside
// path, oldest first.
func DamagedQuarantines(path string) []string {
	if path == "" {
		return nil
	}
	//unchecked: a pattern made of a file name and a literal suffix is always valid
	found, _ := filepath.Glob(path + ".bad-*")
	sort.Strings(found)
	return found
}

// ClearQuarantine removes key from the quarantine kept at path, or every
// entry when key is empty, and returns how many it removed. A damaged file
// is moved aside first and moved names where; clearing every entry of a
// damaged file is then done. A running Guard on the same file reads the
// change at its next check (RES-G3).
func ClearQuarantine(path, key string) (removed int, moved string, err error) {
	if path == "" {
		return 0, "", errors.New("gpufault: no quarantine file")
	}
	g, err := New(Options{State: path})
	if err != nil {
		return 0, "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	moved = g.damaged
	for k := range g.quarantine {
		if key == "" || k == key {
			delete(g.quarantine, k)
			removed++
		}
	}
	if removed == 0 {
		return 0, moved, nil
	}
	return removed, moved, g.saveLocked()
}
