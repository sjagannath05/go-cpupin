package cpupin

import "testing"

// runSteer interprets the subset of classic BPF that buildSteerProgram emits.
func runSteer(t *testing.T, prog []bpfInsn, cpu uint32) uint32 {
	t.Helper()
	var a uint32
	for i := 0; i < len(prog); i++ {
		in := prog[i]
		switch in.Code {
		case opLdAbsW:
			if in.K != skfAdCPU {
				t.Fatalf("insn %d: ld abs K=%#x, want SKF_AD_CPU %#x", i, in.K, uint32(skfAdCPU))
			}
			a = cpu
		case opJeqK:
			if a == in.K {
				i += int(in.Jt)
			} else {
				i += int(in.Jf)
			}
		case opRetK:
			return in.K
		case opModK:
			if in.K == 0 {
				t.Fatal("mod by zero")
			}
			a = a % in.K
		case opRetA:
			return a
		default:
			t.Fatalf("insn %d: unexpected opcode %#x", i, in.Code)
		}
	}
	t.Fatal("fell off the end of the program")
	return 0
}

func TestSteerProgramMapsCoreToIndex(t *testing.T) {
	// Sparse cores — exactly the case where cpu%n mis-steers.
	cores := []int{2, 5, 7}
	prog := buildSteerProgram(cores)
	for i, c := range cores {
		if got := runSteer(t, prog, uint32(c)); got != uint32(i) {
			t.Errorf("cpu %d → socket %d, want %d", c, got, i)
		}
	}
}

func TestSteerProgramOffsetContiguousRegression(t *testing.T) {
	// Contiguous-but-offset cores 2-5: bare modulo would send cpu 2 to
	// socket 2 (2%4) instead of socket 0. The jump chain must not.
	cores := []int{2, 3, 4, 5}
	prog := buildSteerProgram(cores)
	for i, c := range cores {
		if got := runSteer(t, prog, uint32(c)); got != uint32(i) {
			t.Errorf("cpu %d → socket %d, want %d (modulo regression)", c, got, i)
		}
	}
}

func TestSteerProgramFallthroughSpreads(t *testing.T) {
	// CPUs outside the reader set fall through to cpu % n — always in range.
	cores := []int{2, 5, 7}
	prog := buildSteerProgram(cores)
	for cpu := uint32(0); cpu < 64; cpu++ {
		got := runSteer(t, prog, cpu)
		if got >= uint32(len(cores)) {
			t.Fatalf("cpu %d → socket %d, out of range [0,%d)", cpu, got, len(cores))
		}
	}
	if got := runSteer(t, prog, 4); got != 4%3 {
		t.Errorf("fallthrough cpu 4 → %d, want %d", got, 4%3)
	}
}

func TestSteerProgramShape(t *testing.T) {
	cores := []int{0, 1, 2, 3}
	prog := buildSteerProgram(cores)
	if want := 2*len(cores) + 3; len(prog) != want {
		t.Fatalf("program length %d, want %d", len(prog), want)
	}
	if prog[0].Code != opLdAbsW {
		t.Error("program must start with ld abs SKF_AD_CPU")
	}
	last := prog[len(prog)-1]
	if last.Code != opRetA {
		t.Error("program must end with ret A")
	}
}
