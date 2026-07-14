//go:build linux

package cpupin

import (
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPinSelfMovesAndRestores(t *testing.T) {
	avail, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	// Discover, never hardcode: pin to the first available core.
	target := avail.List()[0]

	done := make(chan error, 1)
	go func() {
		unpin, err := PinSelf(target)
		if err != nil {
			done <- err
			return
		}
		var set unix.CPUSet
		if err := unix.SchedGetaffinity(0, &set); err != nil {
			done <- err
			return
		}
		if got := cpuSetFromUnix(&set); !got.Equal(NewCPUSet(target)) {
			unpin()
			done <- errAffinity(got)
			return
		}
		unpin()
		// After unpin the mask must be back to what the thread had before.
		if err := unix.SchedGetaffinity(0, &set); err != nil {
			done <- err
			return
		}
		if got := cpuSetFromUnix(&set); got.Equal(NewCPUSet(target)) && avail.Size() > 1 {
			done <- errRestore(got)
			return
		}
		done <- nil
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type errAffinityT struct {
	got CPUSet
}

func (e errAffinityT) Error() string { return "pinned mask " + e.got.String() + " != requested" }

func errAffinity(got CPUSet) error { return errAffinityT{got} }

type errRestoreT struct{ got CPUSet }

func (e errRestoreT) Error() string { return "unpin did not restore mask, still " + e.got.String() }

func errRestore(got CPUSet) error { return errRestoreT{got} }

func TestPinSelfRejectsUnavailableCore(t *testing.T) {
	avail, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	// A core guaranteed outside the available set.
	bad := avail.List()[avail.Size()-1] + 512
	if _, err := PinSelf(bad); err == nil {
		t.Fatal("PinSelf(unavailable core) must error")
	}
}

func TestPinSelfRejectsEmpty(t *testing.T) {
	if _, err := PinSelf(); err == nil {
		t.Fatal("PinSelf() with no cores must error")
	}
}

func TestPinSelfSet(t *testing.T) {
	// Set-pinning to multiple cores works when >1 core is available.
	avail, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	if avail.Size() < 2 {
		t.Skip("needs >= 2 cores")
	}
	cores := avail.List()[:2]
	done := make(chan error, 1)
	go func() {
		unpin, err := PinSelf(cores...)
		if err != nil {
			done <- err
			return
		}
		defer unpin()
		if got := mustAffinity(); !got.Equal(NewCPUSet(cores...)) {
			done <- errAffinity(got)
			return
		}
		done <- nil
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func mustAffinity() CPUSet {
	var set unix.CPUSet
	if err := unix.SchedGetaffinity(0, &set); err != nil {
		panic(err)
	}
	return cpuSetFromUnix(&set)
}

func TestSetProcessMaskSweepsPreexistingThreads(t *testing.T) {
	avail, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	if avail.Size() < 2 {
		t.Skip("needs >= 2 cores")
	}
	// The whole point of the sweep: a thread created BEFORE
	// SetProcessMask must end up fenced too.
	preborn := make(chan CPUSet)
	release := make(chan struct{})
	go func() {
		// Lock so this goroutine owns one OS thread for the whole test.
		unpinDummy, err := PinSelf(avail.List()...) // lock via PinSelf to full set
		if err != nil {
			close(preborn)
			return
		}
		defer unpinDummy()
		preborn <- mustAffinity() // signal: thread exists, initial mask
		<-release                 // wait while main goroutine sweeps
		preborn <- mustAffinity() // report post-sweep mask
	}()
	if before, ok := <-preborn; !ok || before.IsEmpty() {
		t.Fatal("setup failed")
	}

	fence := NewCPUSet(avail.List()[0])
	if err := SetProcessMask(avail.List()[0]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetProcessMask(avail.List()...) }) // restore

	// Threads created AFTER the sweep inherit the mask. This must run BEFORE
	// close(release): unpinDummy restores a full-width mask and returns that
	// unfenced thread to the pool, which this goroutine could otherwise land on.
	type inheritResult struct {
		set CPUSet
		err error
	}
	inherited := make(chan inheritResult, 1)
	go func() {
		u, err := PinSelf(avail.List()[0]) // lock a fresh thread ...
		if err != nil {
			inherited <- inheritResult{err: err}
			return
		}
		u()
		inherited <- inheritResult{set: mustAffinity()}
	}()
	if res := <-inherited; res.err != nil {
		t.Errorf("post-sweep PinSelf failed: %v", res.err)
	} else if !res.set.Difference(fence).IsEmpty() {
		t.Errorf("post-sweep thread mask = %s, want subset of %s", res.set, fence)
	}

	close(release)
	after := <-preborn
	if !after.Equal(fence) {
		t.Errorf("pre-existing thread mask = %s after sweep, want %s", after, fence)
	}
}

func TestSetProcessMaskRejectsUnavailable(t *testing.T) {
	avail, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	bad := avail.List()[avail.Size()-1] + 512
	if err := SetProcessMask(bad); err == nil {
		t.Fatal("SetProcessMask(unavailable) must error")
	}
}

func TestAvailableStableAfterSetProcessMask(t *testing.T) {
	// Boot-mask regression: the library's own masking must not
	// feed back into discovery.
	avail, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	if avail.Size() < 2 {
		t.Skip("needs >= 2 cores")
	}
	if err := SetProcessMask(avail.List()[0]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetProcessMask(avail.List()...) })

	after, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	if !after.Equal(avail) {
		t.Errorf("Available() changed after SetProcessMask: %s → %s (self-narrowing trap)", avail, after)
	}
}

func TestBuildPinApplyEndToEnd(t *testing.T) {
	avail, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	if avail.Size() < 2 {
		t.Skip("needs >= 2 cores")
	}
	// Register the restore BEFORE Build/Apply: a half-completed Apply must not
	// leak a fenced process mask into subsequent tests.
	t.Cleanup(func() { _ = SetProcessMask(avail.List()...) })
	plan, err := Build(Spec{Roles: []Role{
		{Name: "readers", Threads: 1, Exclusive: true},
		{Name: "housekeeping", Housekeeping: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Apply(); err != nil {
		t.Fatal(err)
	}

	readerCore := plan.Cores("readers").List()[0]
	done := make(chan error, 1)
	go func() {
		unpin, err := plan.Pin("readers", 0)
		if err != nil {
			done <- err
			return
		}
		defer unpin()
		if got := mustAffinity(); !got.Equal(NewCPUSet(readerCore)) {
			done <- errAffinity(got)
			return
		}
		done <- nil
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// Apply set GOMAXPROCS over the FULL set, not the housekeeping subset.
	// With no quota cap it must equal Available().Size();
	// with a quota cap it must still exceed the housekeeping-subset value
	// whenever the quota allows.
	got := runtime.GOMAXPROCS(0)
	q, qok, qerr := QuotaCPUs()
	if qerr != nil {
		t.Fatal(qerr)
	}
	want := avail.Size()
	if qok && int(q) < want {
		want = int(q)
	}
	if want < 1 {
		want = 1
	}
	if got != want {
		t.Errorf("GOMAXPROCS = %d, want %d (full available set, never the housekeeping subset)", got, want)
	}
	// Available() still stable after the whole flow (boot-mask trap).
	after, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	if !after.Equal(avail) {
		t.Errorf("Available() drifted: %s → %s", avail, after)
	}
}
