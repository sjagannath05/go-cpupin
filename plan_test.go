package cpupin

import (
	"strings"
	"testing"
)

func mustBuild(t *testing.T, spec Spec, avail CPUSet, siblings map[int][]int) *Plan {
	t.Helper()
	p, err := buildPlan(spec, avail, siblings)
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	return p
}

func TestPlanBasicAllocation(t *testing.T) {
	avail := NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
	p := mustBuild(t, Spec{Roles: []Role{
		{Name: "readers", Threads: 4, Exclusive: true},
		{Name: "gtpc", Threads: 1},
		{Name: "housekeeping", Housekeeping: true},
	}}, avail, nil)

	if got := p.Cores("readers"); !got.Equal(NewCPUSet(0, 1, 2, 3)) {
		t.Errorf("readers = %s, want 0-3", got)
	}
	if got := p.Cores("gtpc"); !got.Equal(NewCPUSet(4)) {
		t.Errorf("gtpc = %s, want 4", got)
	}
	if got := p.Cores("housekeeping"); !got.Equal(NewCPUSet(5, 6, 7)) {
		t.Errorf("housekeeping = %s, want 5-7", got)
	}
}

func TestPlanSparseAvailable(t *testing.T) {
	// Never assume 0..N-1 (DESIGN §9).
	avail := NewCPUSet(1, 3, 5, 7, 9)
	p := mustBuild(t, Spec{Roles: []Role{
		{Name: "readers", Threads: 3, Exclusive: true},
		{Name: "housekeeping", Housekeeping: true},
	}}, avail, nil)

	if got := p.Cores("readers"); !got.Equal(NewCPUSet(1, 3, 5)) {
		t.Errorf("readers = %s, want 1,3,5", got)
	}
	if got := p.Cores("housekeeping"); !got.Equal(NewCPUSet(7, 9)) {
		t.Errorf("housekeeping = %s, want 7,9", got)
	}
}

func TestPlanExplicitOverridesWin(t *testing.T) {
	avail := NewCPUSet(0, 1, 2, 3)
	p := mustBuild(t, Spec{Roles: []Role{
		{Name: "readers", Threads: 2, Exclusive: true, Cores: []int{2, 3}},
		{Name: "housekeeping", Housekeeping: true},
	}}, avail, nil)

	if got := p.Cores("readers"); !got.Equal(NewCPUSet(2, 3)) {
		t.Errorf("readers = %s, want explicit 2,3", got)
	}
	if got := p.Cores("housekeeping"); !got.Equal(NewCPUSet(0, 1)) {
		t.Errorf("housekeeping = %s, want leftover 0,1", got)
	}
}

func TestPlanExplicitHousekeepingCores(t *testing.T) {
	// INTEGRATION.md config has housekeeping_cores — explicit override allowed.
	avail := NewCPUSet(0, 1, 2, 3)
	p := mustBuild(t, Spec{Roles: []Role{
		{Name: "readers", Threads: 2, Exclusive: true},
		{Name: "housekeeping", Housekeeping: true, Cores: []int{3}},
	}}, avail, nil)
	if got := p.Cores("housekeeping"); !got.Equal(NewCPUSet(3)) {
		t.Errorf("housekeeping = %s, want explicit 3", got)
	}
}

func TestPlanSMTAvoidance(t *testing.T) {
	// 0/1 and 2/3 are sibling pairs. 2 exclusive threads should land on
	// distinct physical cores (0 and 2), not on siblings (0 and 1).
	avail := NewCPUSet(0, 1, 2, 3)
	siblings := map[int][]int{0: {1}, 1: {0}, 2: {3}, 3: {2}}
	p := mustBuild(t, Spec{Roles: []Role{
		{Name: "readers", Threads: 2, Exclusive: true},
		{Name: "housekeeping", Housekeeping: true},
	}}, avail, siblings)

	if got := p.Cores("readers"); !got.Equal(NewCPUSet(0, 2)) {
		t.Errorf("readers = %s, want SMT-avoiding 0,2", got)
	}
	if len(p.warnings) != 0 {
		t.Errorf("unexpected warnings: %v", p.warnings)
	}
}

func TestPlanSMTCollisionWarns(t *testing.T) {
	// 4 exclusive threads on 2 physical cores: collision unavoidable → warning.
	avail := NewCPUSet(0, 1, 2, 3)
	siblings := map[int][]int{0: {1}, 1: {0}, 2: {3}, 3: {2}}
	p, err := buildPlan(Spec{Roles: []Role{
		{Name: "readers", Threads: 4, Exclusive: true},
	}}, avail, siblings)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.warnings) == 0 {
		t.Error("want SMT collision warning, got none")
	}
	if !strings.Contains(p.String(), "SMT") {
		t.Errorf("String() should surface SMT warnings:\n%s", p.String())
	}
}

func TestPlanErrors(t *testing.T) {
	avail := NewCPUSet(0, 1, 2, 3)
	tests := []struct {
		name string
		spec Spec
		frag string // substring the error must contain
	}{
		{"insufficient exclusive", Spec{Roles: []Role{
			{Name: "big", Threads: 6, Exclusive: true},
		}}, "big"},
		{"housekeeping starved", Spec{Roles: []Role{
			{Name: "readers", Threads: 4, Exclusive: true},
			{Name: "housekeeping", Housekeeping: true},
		}}, "housekeeping"},
		{"set-pinned without cores", Spec{Roles: []Role{
			{Name: "workers"},
		}}, "explicit Cores"},
		{"cores outside available", Spec{Roles: []Role{
			{Name: "readers", Threads: 1, Cores: []int{99}},
		}}, "outside"},
		{"duplicate names", Spec{Roles: []Role{
			{Name: "x", Threads: 1}, {Name: "x", Threads: 1},
		}}, "duplicate"},
		{"two housekeeping", Spec{Roles: []Role{
			{Name: "a", Housekeeping: true}, {Name: "b", Housekeeping: true},
		}}, "Housekeeping"},
		{"negative threads", Spec{Roles: []Role{
			{Name: "x", Threads: -1},
		}}, "negative"},
		{"empty name", Spec{Roles: []Role{
			{Name: "", Threads: 1},
		}}, "name"},
		{"overlap needed but not allowed", Spec{Roles: []Role{
			{Name: "a", Threads: 3},
			{Name: "b", Threads: 3},
		}}, "AllowOverlap"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildPlan(tt.spec, avail, nil)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tt.frag) {
				t.Errorf("error %q does not mention %q", err, tt.frag)
			}
		})
	}
}

func TestPlanAllowOverlap(t *testing.T) {
	avail := NewCPUSet(0, 1, 2, 3)
	p := mustBuild(t, Spec{AllowOverlap: true, Roles: []Role{
		{Name: "a", Threads: 3},
		{Name: "b", Threads: 3}, // pool only has 1 fresh core left → wraps
	}}, avail, nil)
	if got := p.Cores("a"); !got.Equal(NewCPUSet(0, 1, 2)) {
		t.Errorf("a = %s, want 0-2", got)
	}
	b := p.Cores("b")
	if !b.Contains(3) {
		t.Errorf("b = %s, must include the fresh core 3", b)
	}
	if b.Size() != 3 {
		t.Errorf("b = %s, want 3 cores (wrapped onto non-exclusive pool)", b)
	}
}

func TestPlanDeterminism(t *testing.T) {
	avail := NewCPUSet(0, 2, 4, 6, 8, 10)
	siblings := map[int][]int{0: {2}, 2: {0}}
	spec := Spec{Roles: []Role{
		{Name: "readers", Threads: 3, Exclusive: true},
		{Name: "gtpc", Threads: 1},
		{Name: "housekeeping", Housekeeping: true},
	}}
	p1 := mustBuild(t, spec, avail, siblings)
	p2 := mustBuild(t, spec, avail, siblings)
	if p1.String() != p2.String() {
		t.Errorf("same spec produced different plans:\n%s\n---\n%s", p1, p2)
	}
}

func TestPlanNoHousekeepingIsFine(t *testing.T) {
	avail := NewCPUSet(0, 1)
	p := mustBuild(t, Spec{Roles: []Role{{Name: "readers", Threads: 1, Exclusive: true}}}, avail, nil)
	if p.housekeeping != "" {
		t.Error("no housekeeping role expected")
	}
}

func TestPlanString(t *testing.T) {
	avail := NewCPUSet(0, 1, 2, 3)
	p := mustBuild(t, Spec{Roles: []Role{
		{Name: "readers", Threads: 2, Exclusive: true},
		{Name: "housekeeping", Housekeeping: true},
	}}, avail, nil)
	s := p.String()
	for _, want := range []string{"available: 0-3", "readers", "exclusive", "0-1", "housekeeping", "2-3"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q:\n%s", want, s)
		}
	}
}

func TestPlanCoresUnknownRole(t *testing.T) {
	avail := NewCPUSet(0, 1)
	p := mustBuild(t, Spec{Roles: []Role{{Name: "a", Threads: 1}}}, avail, nil)
	if got := p.Cores("nope"); !got.IsEmpty() {
		t.Errorf("Cores(unknown) = %s, want empty", got)
	}
}
