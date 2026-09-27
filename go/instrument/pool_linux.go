package instrument

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// machineMemory reads MemTotal, the kernel's own figure for installed memory.
func machineMemory() int64 {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer file.Close()
	lines := bufio.NewScanner(file)
	for lines.Scan() {
		key, value, found := strings.Cut(lines.Text(), ":")
		if !found || key != "MemTotal" {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) != 2 || fields[1] != "kB" {
			return 0
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || kb <= 0 || kb > 1<<52 {
			return 0
		}
		return kb << 10
	}
	return 0
}
