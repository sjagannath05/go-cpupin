package cpupin

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Spec declares the roles an application wants cores allocated for.
type Spec struct {
	Roles        []Role
	AllowOverlap bool // permit non-exclusive roles to share cores when short; default false = error
}

// Role describes one class of pinned work.
type Role struct {
	Name    string
	Threads int // pinned threads this role will run; each gets its own core
	// when possible. 0 ⇒ role shares its core set (set-pinned;
	// requires explicit Cores unless Housekeeping).
	Cores        []int // explicit override from app config; empty ⇒ auto-allocate
	Exclusive    bool  // cores not shared with any other role
	Housekeeping bool  // at most one role: receives all leftover cores AND
	// becomes the process mask on Apply (SetProcessMask)
}

type planRole struct {
	name      string
	threads   int
	exclusive bool
	cores     CPUSet
}

// Plan is a deterministic, printable core allocation. Build once at startup.
type Plan struct {
	available    CPUSet
	roles        []planRole // declaration order
	index        map[string]int
	housekeeping string // role name; "" if none
	warnings     []string
}

// buildPlan is the pure allocator — no syscalls, testable
// anywhere. siblings maps core → SMT siblings (excluding itself); may be nil.
func buildPlan(spec Spec, available CPUSet, siblings map[int][]int) (*Plan, error) {
	if available.IsEmpty() {
		return nil, errors.New("cpupin: no available cores")
	}
	p := &Plan{available: available, index: map[string]int{}}

	// Validation pass.
	for _, r := range spec.Roles {
		if r.Name == "" {
			return nil, errors.New("cpupin: role with empty name")
		}
		if _, dup := p.index[r.Name]; dup {
			return nil, fmt.Errorf("cpupin: duplicate role %q", r.Name)
		}
		if r.Threads < 0 {
			return nil, fmt.Errorf("cpupin: role %q: negative Threads", r.Name)
		}
		if r.Housekeeping {
			if p.housekeeping != "" {
				return nil, fmt.Errorf("cpupin: roles %q and %q both set Housekeeping; at most one may", p.housekeeping, r.Name)
			}
			p.housekeeping = r.Name
		}
		if r.Threads == 0 && len(r.Cores) == 0 && !r.Housekeeping {
			return nil, fmt.Errorf("cpupin: role %q: set-pinned roles (Threads=0) need explicit Cores", r.Name)
		}
		p.index[r.Name] = len(p.roles)
		p.roles = append(p.roles, planRole{name: r.Name, threads: r.Threads, exclusive: r.Exclusive})
	}

	remaining := available

	// 1. Explicit overrides win.
	for _, r := range spec.Roles {
		if len(r.Cores) == 0 {
			continue
		}
		set := NewCPUSet(r.Cores...)
		if bad := set.Difference(available); !bad.IsEmpty() {
			return nil, fmt.Errorf("cpupin: role %q: cores %s outside available set %s", r.Name, bad, available)
		}
		p.roles[p.index[r.Name]].cores = set
		// Explicitly claimed cores are spoken for: auto-allocation (exclusive,
		// shared, and housekeeping leftovers) must not land on them.
		remaining = remaining.Difference(set)
	}

	// 2. Exclusive threaded roles, SMT-sibling-aware.
	exclusiveTaken := NewCPUSet()
	for _, r := range spec.Roles {
		if r.Exclusive && len(r.Cores) > 0 {
			exclusiveTaken = exclusiveTaken.Union(NewCPUSet(r.Cores...))
		}
	}
	for _, r := range spec.Roles {
		if len(r.Cores) > 0 || !r.Exclusive || r.Threads == 0 {
			continue
		}
		if remaining.Size() < r.Threads {
			return nil, fmt.Errorf("cpupin: role %q wants %d exclusive cores, only %d available (of %s) after earlier roles",
				r.Name, r.Threads, remaining.Size(), available)
		}
		picked := pickExclusive(remaining, r.Threads, siblings, exclusiveTaken)
		set := NewCPUSet(picked...)
		p.roles[p.index[r.Name]].cores = set
		exclusiveTaken = exclusiveTaken.Union(set)
		remaining = remaining.Difference(set)
	}

	// 3. Non-exclusive threaded roles.
	nonexPool := remaining // snapshot: what non-exclusive roles may share under AllowOverlap
	for _, r := range spec.Roles {
		if len(r.Cores) > 0 || r.Exclusive || r.Threads == 0 || r.Housekeeping {
			continue
		}
		take := remaining.List()
		if len(take) > r.Threads {
			take = take[:r.Threads]
		}
		if len(take) < r.Threads {
			if !spec.AllowOverlap {
				return nil, fmt.Errorf("cpupin: role %q wants %d cores, only %d left of %s; set AllowOverlap to share",
					r.Name, r.Threads, len(take), available)
			}
			have := NewCPUSet(take...)
			for _, c := range nonexPool.List() {
				if len(take) == r.Threads {
					break
				}
				if !have.Contains(c) {
					take = append(take, c)
					have = have.Union(NewCPUSet(c))
				}
			}
			if len(take) == 0 {
				return nil, fmt.Errorf("cpupin: role %q: no non-exclusive cores available at all", r.Name)
			}
		}
		set := NewCPUSet(take...)
		if set.Size() < r.Threads {
			p.warnings = append(p.warnings,
				fmt.Sprintf("role %q wants %d threads but only %d cores are available to it — threads will share via idx%%len",
					r.Name, r.Threads, set.Size()))
		}
		p.roles[p.index[r.Name]].cores = set
		remaining = remaining.Difference(set)
	}

	// 4. Housekeeping gets the leftovers (unless explicitly overridden).
	if p.housekeeping != "" && p.roles[p.index[p.housekeeping]].cores.IsEmpty() {
		if remaining.IsEmpty() {
			return nil, fmt.Errorf("cpupin: housekeeping role %q: no cores left over (available %s fully consumed)",
				p.housekeeping, available)
		}
		p.roles[p.index[p.housekeeping]].cores = remaining
	}

	// 5. Exclusivity validation: a role marked Exclusive must not share any
	// core with any other role. Overlap between two non-exclusive roles is
	// deliberate sharing
	// (e.g. AllowOverlap wrap) and stays allowed.
	for i := 0; i < len(p.roles); i++ {
		for j := i + 1; j < len(p.roles); j++ {
			if !p.roles[i].exclusive && !p.roles[j].exclusive {
				continue
			}
			if overlap := p.roles[i].cores.Intersect(p.roles[j].cores); !overlap.IsEmpty() {
				culprit := p.roles[i].name
				if !p.roles[i].exclusive {
					culprit = p.roles[j].name
				}
				return nil, fmt.Errorf("cpupin: roles %q and %q overlap on cores %s, but %q is exclusive",
					p.roles[i].name, p.roles[j].name, overlap, culprit)
			}
		}
	}

	// 6. SMT collision warnings across all exclusive assignments.
	for _, c := range exclusiveTaken.List() {
		for _, s := range siblings[c] {
			if s > c && exclusiveTaken.Contains(s) {
				p.warnings = append(p.warnings,
					fmt.Sprintf("cores %d and %d are SMT siblings — exclusive roles share a physical core", c, s))
			}
		}
	}
	return p, nil
}

// pickExclusive chooses n cores from pool ascending, preferring cores whose
// SMT siblings are not already used by exclusive allocations; shortfall falls
// back to plain ascending. pool.Size() >= n is the caller's responsibility.
func pickExclusive(pool CPUSet, n int, siblings map[int][]int, avoid CPUSet) []int {
	picked := make([]int, 0, n)
	used := avoid
	for _, c := range pool.List() {
		if len(picked) == n {
			break
		}
		if siblingIn(c, siblings, used) {
			continue
		}
		picked = append(picked, c)
		used = used.Union(NewCPUSet(c))
	}
	if len(picked) < n {
		have := NewCPUSet(picked...)
		for _, c := range pool.List() {
			if len(picked) == n {
				break
			}
			if !have.Contains(c) {
				picked = append(picked, c)
				have = have.Union(NewCPUSet(c))
			}
		}
	}
	sort.Ints(picked)
	return picked
}

func siblingIn(core int, siblings map[int][]int, set CPUSet) bool {
	for _, s := range siblings[core] {
		if set.Contains(s) {
			return true
		}
	}
	return false
}

// Cores returns the role's allocated core set (zero CPUSet for unknown roles).
func (p *Plan) Cores(role string) CPUSet {
	if i, ok := p.index[role]; ok {
		return p.roles[i].cores
	}
	return CPUSet{}
}

// Build discovers the available cores and SMT topology, then allocates per
// spec. Off-Linux this returns ErrUnsupported (via Available).
func Build(spec Spec) (*Plan, error) {
	avail, err := Available()
	if err != nil {
		return nil, err
	}
	return buildPlan(spec, avail, readSiblings(avail))
}

// Pin pins the calling goroutine per the plan. Threaded roles pin thread idx
// to its own core (idx wraps: idx % len(cores)); set-pinned roles (Threads=0)
// pin to the role's whole core set. Must be called from inside the goroutine
// being pinned, after Apply().
func (p *Plan) Pin(role string, idx int) (Unpin, error) {
	i, ok := p.index[role]
	if !ok {
		return nil, fmt.Errorf("cpupin: Pin: unknown role %q", role)
	}
	r := p.roles[i]
	if r.cores.IsEmpty() {
		return nil, fmt.Errorf("cpupin: Pin: role %q has no cores", role)
	}
	if r.threads > 0 {
		if idx < 0 {
			return nil, fmt.Errorf("cpupin: Pin: role %q: negative thread index %d", role, idx)
		}
		cores := r.cores.List()
		return PinSelf(cores[idx%len(cores)])
	}
	return PinSelf(r.cores.List()...)
}

// Apply fences housekeeping (all-thread process mask sweep) and aligns
// GOMAXPROCS over the FULL available set — never the housekeeping subset;
// pinned datapath threads still need Ps. Call early in main(),
// strictly before any Pin().
func (p *Plan) Apply() error {
	if p.housekeeping != "" {
		if err := SetProcessMask(p.Cores(p.housekeeping).List()...); err != nil {
			return fmt.Errorf("cpupin: Apply: housekeeping mask: %w", err)
		}
	}
	if _, err := SetGOMAXPROCS(); err != nil {
		return fmt.Errorf("cpupin: Apply: gomaxprocs: %w", err)
	}
	return nil
}

// String renders a log-friendly allocation table.
func (p *Plan) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "cpupin plan (available: %s)\n", p.available)
	for _, r := range p.roles {
		kind := "shared"
		switch {
		case r.name == p.housekeeping:
			kind = "housekeeping"
		case r.exclusive:
			kind = "exclusive"
		}
		fmt.Fprintf(&b, "  role %-16s %-12s threads=%-3d cores=%s\n", r.name, kind, r.threads, r.cores)
	}
	for _, w := range p.warnings {
		fmt.Fprintf(&b, "  warning: %s\n", w)
	}
	return b.String()
}
