#!/usr/bin/env bash
#
# run-ab.sh - A/B benchmark harness for go-cpupin inside Docker.
#
#   Arm A (baseline): udpecho -pin=false -steer=false
#   Arm B (pinned):   udpecho -pin=true  -steer=true
#
# Each arm runs the server in its own container, drives load against it
# (locally in a second container, or externally from another host), then
# stops the server with SIGTERM so it emits FINAL per-socket packet counts
# and SO_INCOMING_CPU delivering-CPU histograms. Logs are collected under
# bench/docker/results/<timestamp>/ and summarized side by side.
#
# Usage:
#   bench/docker/run-ab.sh [options]
#
# Options (all optional):
#   --network NAME    Docker network to use (default: cpupin-bench).
#                     Created as a plain bridge if missing (and deleted on
#                     exit); if it already exists it is used as-is and NOT
#                     deleted.
#   --cpuset CPUS     Value for `docker run --cpuset-cpus` on the server
#                     container (default: empty = no constraint).
#   --readers N       Number of reader sockets/goroutines (default: 2).
#   --port P          UDP port the server listens on (default: 9999).
#   --pps N           Load generator target packets/sec (default: 5000).
#   --flows N         Load generator flow count (default: 8).
#   --duration SECS   Load duration in seconds (default: 30).
#   --size BYTES      Payload size in bytes (default: 128).
#   --loadgen MODE    "local" (default): run udploadgen in a second
#                     container on the same bridge. "external": print the
#                     server IP + exact udploadgen command and wait for
#                     Enter while you drive load from another host.
#   --image TAG       Image tag to build/run (default: go-cpupin-bench:latest).
#   --no-build        Skip the docker build step.
#   -h | --help       Show this help.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
NETWORK="cpupin-bench"
CPUSET=""
READERS=2
PORT=9999
PPS=5000
FLOWS=8
DURATION=30
SIZE=128
LOADGEN="local"
IMAGE="go-cpupin-bench:latest"
BUILD=1

usage() {
    cat <<'EOF'
run-ab.sh - A/B benchmark harness for go-cpupin inside Docker.

  Arm A (baseline): udpecho -pin=false -steer=false
  Arm B (pinned):   udpecho -pin=true  -steer=true

Usage: bench/docker/run-ab.sh [options]

Options (all optional):
  --network NAME    Docker network to use (default: cpupin-bench).
                    Created as a plain bridge if missing (and deleted on
                    exit); if it already exists it is used as-is and NOT
                    deleted.
  --cpuset CPUS     Value for `docker run --cpuset-cpus` on the server
                    container (default: empty = no constraint).
  --readers N       Number of reader sockets/goroutines (default: 2).
  --port P          UDP port the server listens on (default: 9999).
  --pps N           Load generator target packets/sec (default: 5000).
  --flows N         Load generator flow count (default: 8).
  --duration SECS   Load duration in seconds (default: 30).
  --size BYTES      Payload size in bytes (default: 128).
  --loadgen MODE    "local" (default): run udploadgen in a second
                    container on the same bridge. "external": print the
                    server IP + exact udploadgen command and wait for
                    Enter while you drive load from another host.
  --image TAG       Image tag to build/run (default: go-cpupin-bench:latest).
  --no-build        Skip the docker build step.
  -h | --help       Show this help.

Logs land in bench/docker/results/<timestamp>/arm-{A,B}-{server,loadgen}.log
EOF
}

# ---------------------------------------------------------------------------
# Argument parsing (supports both "--opt value" and "--opt=value")
# ---------------------------------------------------------------------------
need_val() {
    if [[ $# -lt 2 ]]; then
        echo "ERROR: $1 requires a value" >&2
        exit 1
    fi
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --network)    need_val "$@"; NETWORK="$2";  shift 2 ;;
        --network=*)  NETWORK="${1#*=}";  shift ;;
        --cpuset)     need_val "$@"; CPUSET="$2";   shift 2 ;;
        --cpuset=*)   CPUSET="${1#*=}";   shift ;;
        --readers)    need_val "$@"; READERS="$2";  shift 2 ;;
        --readers=*)  READERS="${1#*=}";  shift ;;
        --port)       need_val "$@"; PORT="$2";     shift 2 ;;
        --port=*)     PORT="${1#*=}";     shift ;;
        --pps)        need_val "$@"; PPS="$2";      shift 2 ;;
        --pps=*)      PPS="${1#*=}";      shift ;;
        --flows)      need_val "$@"; FLOWS="$2";    shift 2 ;;
        --flows=*)    FLOWS="${1#*=}";    shift ;;
        --duration)   need_val "$@"; DURATION="$2"; shift 2 ;;
        --duration=*) DURATION="${1#*=}"; shift ;;
        --size)       need_val "$@"; SIZE="$2";     shift 2 ;;
        --size=*)     SIZE="${1#*=}";     shift ;;
        --loadgen)    need_val "$@"; LOADGEN="$2";  shift 2 ;;
        --loadgen=*)  LOADGEN="${1#*=}";  shift ;;
        --image)      need_val "$@"; IMAGE="$2";    shift 2 ;;
        --image=*)    IMAGE="${1#*=}";    shift ;;
        --no-build)   BUILD=0; shift ;;
        -h|--help)    usage; exit 0 ;;
        *)
            echo "ERROR: unknown argument: $1" >&2
            usage >&2
            exit 1
            ;;
    esac
done

case "$LOADGEN" in
    local|external) ;;
    *)
        echo "ERROR: --loadgen must be 'local' or 'external' (got '$LOADGEN')" >&2
        exit 1
        ;;
esac

if ! command -v docker >/dev/null 2>&1; then
    echo "ERROR: docker not found on PATH" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

# Echo every docker invocation (to stderr, so command substitution stays
# clean) then run it — keeps runs auditable.
dockerx() {
    echo "+ docker $*" >&2
    docker "$@"
}

# ---------------------------------------------------------------------------
# Cleanup (EXIT trap)
# ---------------------------------------------------------------------------
CREATED_NETWORK=0
CREATED_CONTAINERS=()

cleanup() {
    local c
    for c in ${CREATED_CONTAINERS[@]+"${CREATED_CONTAINERS[@]}"}; do
        docker rm -f "$c" >/dev/null 2>&1 || true
    done
    if [[ "$CREATED_NETWORK" -eq 1 ]]; then
        # Only delete the network if THIS run created it. Pre-existing
        # networks (e.g. a shared/production bridge) are left untouched.
        docker network rm "$NETWORK" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Network
# ---------------------------------------------------------------------------
if docker network inspect "$NETWORK" >/dev/null 2>&1; then
    echo "Using existing docker network '$NETWORK' (will NOT be deleted)."
else
    dockerx network create --driver bridge "$NETWORK" >/dev/null
    CREATED_NETWORK=1
    echo "Created docker network '$NETWORK' (will be deleted on exit)."
fi

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------
if [[ "$BUILD" -eq 1 ]]; then
    dockerx build -f "${SCRIPT_DIR}/Dockerfile" -t "$IMAGE" "$REPO_ROOT"
else
    echo "Skipping image build (--no-build); using '$IMAGE'."
fi

# ---------------------------------------------------------------------------
# Results directory
# ---------------------------------------------------------------------------
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
RESULTS_DIR="${SCRIPT_DIR}/results/${TIMESTAMP}"
mkdir -p "$RESULTS_DIR"
echo "Results directory: $RESULTS_DIR"

if [[ "$LOADGEN" == "local" ]]; then
    cat <<'EOF'

############################################################################
# CAVEAT: --loadgen local sends traffic over the SAME Docker bridge.       #
# This exercises the veth + bridge path but NOT the physical NIC's RSS     #
# hashing, so it is only a partial locality test. For the real answer to   #
# "does steering align with RSS?", run with --loadgen external and drive   #
# udploadgen from a different host so packets arrive via the NIC.          #
############################################################################

EOF
fi

# ---------------------------------------------------------------------------
# Run one arm: run_arm <A|B> <pin bool> <steer bool>
# ---------------------------------------------------------------------------
run_arm() {
    local arm="$1" pin="$2" steer="$3"
    local server="cpupin-bench-${arm}"
    local server_log="${RESULTS_DIR}/arm-${arm}-server.log"
    local loadgen_log="${RESULTS_DIR}/arm-${arm}-loadgen.log"

    echo ""
    echo "=== Arm ${arm}: -pin=${pin} -steer=${steer} ==="

    # Remove any leftover container of the same name from a previous run.
    docker rm -f "$server" >/dev/null 2>&1 || true
    CREATED_CONTAINERS+=("$server")

    local run_args=(run -d --name "$server" --network "$NETWORK")
    if [[ -n "$CPUSET" ]]; then
        run_args+=(--cpuset-cpus "$CPUSET")
    fi
    run_args+=("$IMAGE" /udpecho
        -addr ":${PORT}"
        -readers "$READERS"
        -pin="$pin"
        -steer="$steer"
        -cpustats=true)

    dockerx "${run_args[@]}" >/dev/null

    # Give the server a moment to bind and print its MODE line.
    sleep 2
    if [[ "$(docker inspect -f '{{.State.Running}}' "$server" 2>/dev/null)" != "true" ]]; then
        echo "ERROR: arm ${arm} server container failed to start. Logs:" >&2
        docker logs "$server" >&2 || true
        exit 1
    fi
    docker logs "$server" 2>&1 | grep -m1 '^MODE' || true

    local ip
    ip="$(dockerx inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$server")"
    if [[ -z "$ip" ]]; then
        echo "ERROR: could not determine IP of container '$server' on network '$NETWORK'" >&2
        exit 1
    fi
    echo "Arm ${arm} server IP: ${ip}"

    if [[ "$LOADGEN" == "local" ]]; then
        echo "NOTE: local loadgen shares the bridge with the server;" \
             "NIC RSS is NOT exercised."
        dockerx run --rm --network "$NETWORK" "$IMAGE" /udploadgen \
            -addr "${ip}:${PORT}" \
            -flows "$FLOWS" \
            -pps "$PPS" \
            -duration "${DURATION}s" \
            -size "$SIZE" | tee "$loadgen_log"
    else
        cat <<EOF

--- Arm ${arm}: EXTERNAL loadgen mode ---
Server container IP on network '$NETWORK': ${ip}:${PORT}/udp

From the load-generating host, run:

  udploadgen -addr ${ip}:${PORT} -flows ${FLOWS} -pps ${PPS} -duration ${DURATION}s -size ${SIZE}

(The container IP must be reachable from that host — e.g. a routed or
macvlan network. On a default isolated bridge it will not be.)
EOF
        read -r -p "Press Enter when the external load run for arm ${arm} is complete... " _ || true
        : > "$loadgen_log"  # no local RESULT line in external mode
    fi

    # SIGTERM -> udpecho prints FINAL per-socket lines before exiting.
    dockerx stop -t 15 "$server" >/dev/null
    dockerx logs "$server" >"$server_log" 2>&1
    dockerx rm "$server" >/dev/null

    echo "Arm ${arm} logs: ${server_log}" \
         "$( [[ "$LOADGEN" == "local" ]] && echo "and ${loadgen_log}" )"
}

run_arm A false false
run_arm B true  true

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

# Extract "field=value" from the single RESULT line of a loadgen log.
result_field() {
    local log="$1" field="$2" val=""
    if [[ -s "$log" ]]; then
        val="$(grep -m1 '^RESULT ' "$log" | grep -o "${field}=[^ ]*" | head -n1 | cut -d= -f2 || true)"
    fi
    echo "${val:-n/a}"
}

# Extract pkts=N from the "FINAL total" line of a server log.
final_total() {
    local val
    val="$(grep -m1 '^FINAL total' "$1" | grep -o 'pkts=[^ ]*' | head -n1 | cut -d= -f2 || true)"
    echo "${val:-n/a}"
}

# max/min ratio of per-socket FINAL pkts (spread; 1.00 = perfectly even).
spread_ratio() {
    awk '
        /^FINAL socket=/ {
            for (i = 1; i <= NF; i++) {
                if ($i ~ /^pkts=/) {
                    v = substr($i, 6) + 0
                    if (min == "" || v < min) min = v
                    if (v > max) max = v
                }
            }
        }
        END {
            if (min == "")      print "n/a"
            else if (min == 0)  print "inf"
            else                printf "%.2f\n", max / min
        }' "$1"
}

A_SRV="${RESULTS_DIR}/arm-A-server.log"
B_SRV="${RESULTS_DIR}/arm-B-server.log"
A_LG="${RESULTS_DIR}/arm-A-loadgen.log"
B_LG="${RESULTS_DIR}/arm-B-loadgen.log"

echo ""
echo "============================== SUMMARY =============================="
printf '%-22s %-22s %-22s\n' "metric" "A (baseline)" "B (pin+steer)"
printf '%-22s %-22s %-22s\n' "----------------------" "--------------------" "--------------------"
for f in pps loss_pct rtt_p50_us rtt_p95_us rtt_p99_us; do
    printf '%-22s %-22s %-22s\n' "$f" "$(result_field "$A_LG" "$f")" "$(result_field "$B_LG" "$f")"
done
printf '%-22s %-22s %-22s\n' "total pkts (server)" "$(final_total "$A_SRV")" "$(final_total "$B_SRV")"
printf '%-22s %-22s %-22s\n' "spread (max/min)" "$(spread_ratio "$A_SRV")" "$(spread_ratio "$B_SRV")"

echo ""
echo "--- Arm A per-socket FINAL lines (delivering-CPU histograms) ---"
grep '^FINAL socket=' "$A_SRV" || echo "(no FINAL socket lines found in $A_SRV)"
echo ""
echo "--- Arm B per-socket FINAL lines (delivering-CPU histograms) ---"
grep '^FINAL socket=' "$B_SRV" || echo "(no FINAL socket lines found in $B_SRV)"

cat <<'EOF'

--- How to read this ---
* spread (max/min): per-socket packet balance. Near 1.00 in arm B means the
  REUSEPORT cBPF steering distributed flows evenly across sockets.
* cpus=... histograms: which softirq CPU delivered each socket's packets
  (SO_INCOMING_CPU). In arm B, each socket should be DOMINATED BY ONE CPU,
  equal to the core its reader is pinned to — that means delivering CPU and
  consuming thread are aligned (locality holds across the container netns).
* If arm B's histograms look like arm A's (multiple CPUs per socket),
  locality is broken: steering degraded to a plain load spreader. Expected
  causes: traffic entering via veth/bridge (local mode — NIC RSS never ran),
  or IRQ/RPS placement not matching the pinned cores.
EOF

if [[ "$LOADGEN" == "local" ]]; then
    echo ""
    echo "REMINDER: this run used --loadgen local (same-bridge traffic)."
    echo "NIC RSS was NOT exercised; use --loadgen external from another"
    echo "host for the real RSS-alignment answer."
fi

echo ""
echo "Raw logs: $RESULTS_DIR"
