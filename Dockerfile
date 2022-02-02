# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.17 as builder

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
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-X github.com/piraeusdatastore/piraeus-ha-controller/pkg/consts.Version=${VERSION} -extldflags=-static"  -v ./cmd/...

FROM --platform=$BUILDPLATFORM golang:1.17 as downloader

ARG TARGETOS
ARG TARGETARCH
ARG LINSTOR_WAIT_UNTIL_VERSION=v0.1.1
RUN curl -fsSL https://github.com/LINBIT/linstor-wait-until/releases/download/$LINSTOR_WAIT_UNTIL_VERSION/linstor-wait-until-$LINSTOR_WAIT_UNTIL_VERSION-$TARGETOS-$TARGETARCH.tar.gz | tar xvzC /

FROM gcr.io/distroless/static:latest
COPY --from=builder /src/piraeus-ha-controller /piraeus-ha-controller
COPY --from=downloader /linstor-wait-until /linstor-wait-until
USER nonroot
ENTRYPOINT ["/piraeus-ha-controller"]
