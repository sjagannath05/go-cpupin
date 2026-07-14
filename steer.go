package cpupin

// Classic-BPF opcode constants (stable kernel ABI). Defined locally so the
// program builder is portable and unit-testable off-Linux; the Linux attach
// path converts to unix.SockFilter (steer_linux.go).
const (
	opLdAbsW = 0x20 // BPF_LD | BPF_W | BPF_ABS
	opJeqK   = 0x15 // BPF_JMP | BPF_JEQ | BPF_K
	opRetK   = 0x06 // BPF_RET | BPF_K
	opRetA   = 0x16 // BPF_RET | BPF_A
	opModK   = 0x94 // BPF_ALU | BPF_MOD | BPF_K

	// skfAdCPU is the ancillary-data load offset for the delivering CPU:
	// SKF_AD_OFF (-0x1000, as u32) + SKF_AD_CPU (36).
	skfAdCPU = 0xfffff024
)

type bpfInsn struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

// buildSteerProgram emits the cpu→socket-index mapping program:
//
//	ld  #cpu               ; A = delivering CPU (SKF_AD_CPU)
//	jeq #core_0 → ret #0   ; one pair per reader core, socket-index order
//	...
//	mod #n ; ret A         ; fallthrough: spread CPUs outside the reader set
//
// An explicit map, NOT `cpu % n`: modulo is only correct when core IDs ≡
// index (mod n); sparse or offset core sets turn it into deterministic
// misalignment. ~2 insns per core + 3, far under the 4096-insn CBPF limit.
func buildSteerProgram(cores []int) []bpfInsn {
	prog := make([]bpfInsn, 0, 2*len(cores)+3)
	prog = append(prog, bpfInsn{Code: opLdAbsW, K: skfAdCPU})
	for i, c := range cores {
		prog = append(prog,
			bpfInsn{Code: opJeqK, Jt: 0, Jf: 1, K: uint32(c)}, // equal → fall into ret; else skip it
			bpfInsn{Code: opRetK, K: uint32(i)},
		)
	}
	prog = append(prog,
		bpfInsn{Code: opModK, K: uint32(len(cores))},
		bpfInsn{Code: opRetA},
	)
	return prog
}
