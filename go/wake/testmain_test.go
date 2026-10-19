package wake

import (
	"os"
	"testing"

	"github.com/openabstractions/abstraction-resource/go/internal/personfolders"
)

// TestMain keeps every test's per-user folders in a temporary stand-in.
func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }
