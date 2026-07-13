package cpupin

import "testing"

func TestCgroupPaths(t *testing.T) {
	// Real-world shapes: pure v2 (container with cgroupns), hybrid, pure v1.
	tests := []struct {
		name   string
		in     string
		wantV2 string
		wantV1 map[string]string
	}{
		{
			name:   "pure v2 container",
			in:     "0::/\n",
			wantV2: "/",
			wantV1: map[string]string{},
		},
		{
			name:   "v2 host slice",
			in:     "0::/system.slice/docker-abc.scope\n",
			wantV2: "/system.slice/docker-abc.scope",
			wantV1: map[string]string{},
		},
		{
			name: "hybrid v1+v2",
			in: "12:cpuset:/docker/abc\n" +
				"11:cpu,cpuacct:/docker/abc\n" +
				"0::/docker/abc\n",
			wantV2: "/docker/abc",
			wantV1: map[string]string{"cpuset": "/docker/abc", "cpu": "/docker/abc", "cpuacct": "/docker/abc"},
		},
		{
			name: "pure v1",
			in: "12:cpuset:/\n" +
				"11:cpu,cpuacct:/\n",
			wantV2: "",
			wantV1: map[string]string{"cpuset": "/", "cpu": "/", "cpuacct": "/"},
		},
		{
			name:   "garbage lines ignored",
			in:     "not a cgroup line\n\n0::/foo\n",
			wantV2: "/foo",
			wantV1: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v2, v1 := cgroupPaths(tt.in)
			if v2 != tt.wantV2 {
				t.Errorf("v2 = %q, want %q", v2, tt.wantV2)
			}
			if len(v1) != len(tt.wantV1) {
				t.Fatalf("v1 = %v, want %v", v1, tt.wantV1)
			}
			for k, v := range tt.wantV1 {
				if v1[k] != v {
					t.Errorf("v1[%q] = %q, want %q", k, v1[k], v)
				}
			}
		})
	}
}

func TestParseCPUMax(t *testing.T) {
	tests := []struct {
		in      string
		want    float64
		wantOK  bool
		wantErr bool
	}{
		{"max 100000\n", 0, false, false},
		{"200000 100000\n", 2.0, true, false},
		{"50000 100000", 0.5, true, false},
		{"junk", 0, false, true},
		{"a b", 0, false, true},
		{"100000 0", 0, false, true},
	}
	for _, tt := range tests {
		got, ok, err := parseCPUMax(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseCPUMax(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if err == nil && (got != tt.want || ok != tt.wantOK) {
			t.Errorf("parseCPUMax(%q) = (%v, %v), want (%v, %v)", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestParseCFSQuota(t *testing.T) {
	tests := []struct {
		quota, period string
		want          float64
		wantOK        bool
		wantErr       bool
	}{
		{"-1\n", "100000\n", 0, false, false}, // unlimited
		{"200000\n", "100000\n", 2.0, true, false},
		{"50000", "100000", 0.5, true, false},
		{"junk", "100000", 0, false, true},
		{"100000", "0", 0, false, true},
	}
	for _, tt := range tests {
		got, ok, err := parseCFSQuota(tt.quota, tt.period)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseCFSQuota(%q,%q) err = %v, wantErr %v", tt.quota, tt.period, err, tt.wantErr)
			continue
		}
		if err == nil && (got != tt.want || ok != tt.wantOK) {
			t.Errorf("parseCFSQuota(%q,%q) = (%v,%v), want (%v,%v)", tt.quota, tt.period, got, ok, tt.want, tt.wantOK)
		}
	}
}
