# syntax=docker/dockerfile:1.4
FROM --platform=$BUILDPLATFORM golang:1.21 as builder

WORKDIR /src/
COPY go.* /src/
# Cache mod downloads
RUN go mod download -x

COPY cmd /src/cmd
COPY pkg /src/pkg

ARG GOOS=linux
ARG VERSION=v0.0.0-0.unknown

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-X github.com/piraeusdatastore/piraeus-ha-controller/pkg/metadata.Version=${VERSION} -extldflags=-static"  -v ./cmd/agent

FROM debian:bookworm

RUN <<EOF
  set -e
  apt-get update
  apt-get install -y wget ca-certificates gnupg
  { echo 'APT::Install-Recommends "false";' ; echo 'APT::Install-Suggests "false";' ; } > /etc/apt/apt.conf.d/99_piraeus
  wget -O- https://packages.linbit.com/package-signing-pubkey.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/linbit-keyring.gpg
  echo "deb http://packages.linbit.com/public" bookworm "misc" > /etc/apt/sources.list.d/linbit.list
  apt-get update
  apt-get install -y drbd-utils
  apt-get clean -y
  rm -r /var/lib/apt/lists/* /etc/apt/sources.list.d/linbit.list
  echo "global { usage-count no; } " > /etc/drbd.d/global_common.conf
EOF

COPY --from=builder /src/agent /agent
CMD ["/agent", "-v=2"]
