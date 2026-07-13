package cpupin

import (
	"reflect"
	"testing"
)

func TestParseCPUSet(t *testing.T) {
	tests := []struct {
		in      string
		want    []int
		wantErr bool
	}{
		{"", []int{}, false},
		{"  \n", []int{}, false},
		{"0", []int{0}, false},
		{"5", []int{5}, false},
		{"0-3,8-11", []int{0, 1, 2, 3, 8, 9, 10, 11}, false},
		{"0-0", []int{0}, false},
		{"1,2,3", []int{1, 2, 3}, false},
		{" 0-2\n", []int{0, 1, 2}, false},
		{"0-2,2-4", []int{0, 1, 2, 3, 4}, false}, // overlap dedupes
		{"3,1,2", []int{1, 2, 3}, false},         // order normalized
		{"3-1", nil, true},                       // reversed range
		{"a", nil, true},
		{"-1", nil, true},
		{"1,", nil, true}, // trailing empty element
		{"1--3", nil, true},
	}
	for _, tt := range tests {
		got, err := ParseCPUSet(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseCPUSet(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if err == nil && !reflect.DeepEqual(got.List(), tt.want) {
			t.Errorf("ParseCPUSet(%q) = %v, want %v", tt.in, got.List(), tt.want)
		}
	}
}

func TestCPUSetString(t *testing.T) {
	tests := []struct {
		cores []int
		want  string
	}{
		{[]int{}, ""},
		{[]int{5}, "5"},
		{[]int{0, 1, 2, 3, 8, 9, 10, 11}, "0-3,8-11"},
		{[]int{0, 2, 4}, "0,2,4"},
		{[]int{0, 1, 3, 4, 5, 9}, "0-1,3-5,9"},
	}
	for _, tt := range tests {
		if got := NewCPUSet(tt.cores...).String(); got != tt.want {
			t.Errorf("NewCPUSet(%v).String() = %q, want %q", tt.cores, got, tt.want)
		}
	}
}

func TestCPUSetRoundTrip(t *testing.T) {
	for _, s := range []string{"0-3,8-11", "5", "0,2,4,6", "0-7"} {
		set, err := ParseCPUSet(s)
		if err != nil {
			t.Fatalf("ParseCPUSet(%q): %v", s, err)
		}
		if got := set.String(); got != s {
			t.Errorf("round-trip %q → %q", s, got)
		}
	}
}

func TestCPUSetAlgebra(t *testing.T) {
	a := NewCPUSet(0, 1, 2, 3)
	b := NewCPUSet(2, 3, 4, 5)

	if got := a.Intersect(b).List(); !reflect.DeepEqual(got, []int{2, 3}) {
		t.Errorf("Intersect = %v, want [2 3]", got)
	}
	if got := a.Difference(b).List(); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Errorf("Difference = %v, want [0 1]", got)
	}
	if got := a.Union(b).List(); !reflect.DeepEqual(got, []int{0, 1, 2, 3, 4, 5}) {
		t.Errorf("Union = %v, want [0..5]", got)
	}
	if !a.Contains(3) || a.Contains(4) {
		t.Error("Contains wrong")
	}
	if !a.Equal(NewCPUSet(3, 2, 1, 0)) {
		t.Error("Equal should normalize order")
	}
	if a.Equal(b) {
		t.Error("Equal false negative expected")
	}
	var zero CPUSet
	if !zero.IsEmpty() || zero.Size() != 0 || zero.String() != "" {
		t.Error("zero value must be the empty set")
	}
	// NewCPUSet dedupes
	if got := NewCPUSet(1, 1, 2).List(); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Errorf("dedupe failed: %v", got)
	}
}

func TestCPUSetImmutability(t *testing.T) {
	s := NewCPUSet(0, 1, 2)
	l := s.List()
	l[0] = 99
	if !s.Equal(NewCPUSet(0, 1, 2)) {
		t.Error("List() must return a copy — mutating it changed the set")
	}
}
