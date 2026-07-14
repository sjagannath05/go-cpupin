package cpupin

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// CPUSet is an immutable, sorted, deduplicated set of CPU core IDs. Opaque so
// the invariants can't be violated by construction (k8s.io/utils/cpuset is
// the behavioral model). The zero value is the empty set.
type CPUSet struct {
	cores []int // sorted ascending, unique; never mutated after construction
}

// NewCPUSet builds a CPUSet from core IDs, sorting and deduplicating.
func NewCPUSet(cores ...int) CPUSet {
	out := make([]int, 0, len(cores))
	seen := make(map[int]struct{}, len(cores))
	for _, c := range cores {
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	sort.Ints(out)
	return CPUSet{cores: out}
}

// ParseCPUSet parses kernel cpuset list syntax ("0-3,8,11-12").
// Empty or all-whitespace input yields the empty set.
func ParseCPUSet(list string) (CPUSet, error) {
	trimmed := strings.TrimSpace(list)
	if trimmed == "" {
		return CPUSet{}, nil
	}
	var cores []int
	for _, tok := range strings.Split(trimmed, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			return CPUSet{}, fmt.Errorf("cpupin: cpuset list %q: empty element", list)
		}
		lo, hi, isRange := strings.Cut(tok, "-")
		a, err := strconv.Atoi(lo)
		if err != nil || a < 0 {
			return CPUSet{}, fmt.Errorf("cpupin: cpuset list %q: bad core %q", list, tok)
		}
		b := a
		if isRange {
			b, err = strconv.Atoi(hi)
			if err != nil || b < a {
				return CPUSet{}, fmt.Errorf("cpupin: cpuset list %q: bad range %q", list, tok)
			}
		}
		for c := a; c <= b; c++ {
			cores = append(cores, c)
		}
	}
	return NewCPUSet(cores...), nil
}

// List returns the core IDs ascending. Always a fresh copy, never nil.
func (s CPUSet) List() []int {
	out := make([]int, len(s.cores))
	copy(out, s.cores)
	return out
}

// Size returns the number of cores in the set.
func (s CPUSet) Size() int { return len(s.cores) }

// IsEmpty reports whether the set has no cores.
func (s CPUSet) IsEmpty() bool { return len(s.cores) == 0 }

// Contains reports whether core is in the set.
func (s CPUSet) Contains(core int) bool {
	i := sort.SearchInts(s.cores, core)
	return i < len(s.cores) && s.cores[i] == core
}

// Intersect returns the cores present in both sets.
func (s CPUSet) Intersect(o CPUSet) CPUSet {
	out := make([]int, 0, len(s.cores))
	for _, c := range s.cores {
		if o.Contains(c) {
			out = append(out, c)
		}
	}
	return CPUSet{cores: out}
}

// Difference returns the cores in s that are not in o.
func (s CPUSet) Difference(o CPUSet) CPUSet {
	out := make([]int, 0, len(s.cores))
	for _, c := range s.cores {
		if !o.Contains(c) {
			out = append(out, c)
		}
	}
	return CPUSet{cores: out}
}

// Union returns the cores present in either set.
func (s CPUSet) Union(o CPUSet) CPUSet {
	return NewCPUSet(append(s.List(), o.List()...)...)
}

// Equal reports whether both sets contain exactly the same cores.
func (s CPUSet) Equal(o CPUSet) bool {
	if len(s.cores) != len(o.cores) {
		return false
	}
	for i := range s.cores {
		if s.cores[i] != o.cores[i] {
			return false
		}
	}
	return true
}

// String formats the set in kernel list syntax ("0-3,8,11-12"); "" when empty.
func (s CPUSet) String() string {
	if len(s.cores) == 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(s.cores); {
		j := i
		for j+1 < len(s.cores) && s.cores[j+1] == s.cores[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		if j == i {
			fmt.Fprintf(&b, "%d", s.cores[i])
		} else {
			fmt.Fprintf(&b, "%d-%d", s.cores[i], s.cores[j])
		}
		i = j + 1
	}
	return b.String()
}
