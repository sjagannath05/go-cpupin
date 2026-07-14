# go-cpupin

CPU pinning and packet-path alignment for Go on Linux: cgroup-aware core
discovery, role-based core planning, thread pinning, SO_REUSEPORT cpu→index
steering, and read-only RSS/IRQ alignment diagnostics.

Only dependency: `golang.org/x/sys`. `CGO_ENABLED=0`. Off-Linux, everything
returns `ErrUnsupported` (gate with `cpupin.Supported()`); `CPUSet` and the
planner logic are portable and fully unit-tested on any OS (the public
`Build()` needs Linux for discovery).

```
go get github.com/sjagannath05/go-cpupin
```

## Quick start

```go
plan, err := cpupin.Build(cpupin.Spec{Roles: []cpupin.Role{
    {Name: "readers", Threads: 4, Exclusive: true},
    {Name: "housekeeping", Housekeeping: true},
}})
if err != nil { log.Fatal(err) }          // wrong plans fail loudly at startup
if err := plan.Apply(); err != nil { ... } // all-thread mask sweep + GOMAXPROCS
log.Print(plan)                            // printable allocation table

// inside each reader goroutine, after Apply():
unpin, err := plan.Pin("readers", idx)

// after binding REUSEPORT reader sockets in index order:
err = cpupin.SteerReuseport(fd, plan.Cores("readers"))
```

## Deployment notes

- Use `docker --cpuset-cpus`, not `--cpus` — quota is invisible to affinity
  syscalls. systemd `AllowedCPUs=`/`CPUAffinity=` are the equivalent filter.
- `SKF_AD_CPU` locality through bridge/veth networking is "happens to work",
  not contract — run `example/udpecho` in your deployment shape to verify.
  Host prep (IRQ pinning, `ethtool -L`, irqbalance bans) is deliberately out
  of scope for the library: mutating host IRQ state from inside an
  app/container is the wrong layer.

## Measured results

See [bench/RESULTS.md](bench/RESULTS.md): steering's CPU signal verified through
bridge+veth networking, the ~10-20kpps crossover where pinning flips from a small
tail tax to a categorical win (0% vs 16-19% loss at 50kpps), and per-host-class
guidance on when to enable what.

## example/udpecho

End-to-end smoke rig: `go run ./example/udpecho -readers 4 -iface eth0` on a
Linux box, then blast UDP from several source ports — per-socket counters
should spread evenly when steering works. Needs at least readers+1 cores
available (4 readers ⇒ ≥5 cores, one left for housekeeping); on small boxes
try `-readers 2`.
