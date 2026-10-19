package instrument

import "testing"

// AdapterUnified answers for this machine's largest hardware adapter; on a
// machine with none it says so with an error.
func TestAdapterUnifiedAnswers(t *testing.T) {
	got, err := AdapterUnified()
	if err != nil {
		t.Skipf("no answer on this machine: %v", err)
	}
	again, err := AdapterUnified()
	if err != nil || again != got {
		t.Fatalf("a second read answered %v, %v", again, err)
	}
	t.Logf("card:0 unified memory: %v", got)
}
