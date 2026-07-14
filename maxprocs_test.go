package cpupin

import (
	"runtime"
	"testing"
)

func TestSetGOMAXPROCS(t *testing.T) {
	before := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(before)

	n, err := SetGOMAXPROCS()
	if !Supported() {
		// Documented no-op: returns current value, nil error.
		if err != nil {
			t.Fatalf("off-Linux SetGOMAXPROCS err = %v, want nil", err)
		}
		if n != before {
			t.Errorf("off-Linux SetGOMAXPROCS = %d, want current %d", n, before)
		}
		return
	}
	if err != nil {
		t.Fatalf("SetGOMAXPROCS: %v", err)
	}
	if n < 1 {
		t.Errorf("SetGOMAXPROCS = %d, want >= 1", n)
	}
	if got := runtime.GOMAXPROCS(0); got != n {
		t.Errorf("GOMAXPROCS now %d, want %d (return value must be what was set)", got, n)
	}
	avail, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	if n > avail.Size() {
		t.Errorf("SetGOMAXPROCS = %d > available %d", n, avail.Size())
	}
}
