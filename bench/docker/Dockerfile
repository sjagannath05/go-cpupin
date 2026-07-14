# A/B benchmark image for go-cpupin.
#
# Build context is the REPO ROOT (go.mod lives there):
#   docker build -f bench/docker/Dockerfile -t go-cpupin-bench:latest .
#
# Stage 1 builds two static binaries:
#   /udpecho     - SO_REUSEPORT UDP echo server (pin/steer flags)
#   /udploadgen  - UDP load generator (prints one RESULT line)
# Stage 2 is a minimal distroless image with both binaries and no
# ENTRYPOINT; the harness (run-ab.sh) passes the full command.

FROM golang:1.25 AS build

WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/udpecho ./example/udpecho && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/udploadgen ./example/udploadgen

# Runtime image. Runs as root inside the container so that
# sched_setaffinity / setsockopt paths have no permission surprises;
# the container itself needs no extra capabilities.
FROM gcr.io/distroless/static-debian12

COPY --from=build /out/udpecho /udpecho
COPY --from=build /out/udploadgen /udploadgen

# No ENTRYPOINT / CMD on purpose: the harness supplies the command,
# e.g. `docker run <image> /udpecho -addr :9999 -readers 2 -pin=true ...`
