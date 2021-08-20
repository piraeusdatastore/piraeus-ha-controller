FROM golang:1.17 as builder

WORKDIR /src/
COPY go.* /src/
# Cache mod downloads
RUN go mod download -x

COPY cmd /src/cmd
COPY pkg /src/pkg

ARG GOOS=linux
ARG VERSION=v0.0.0-0.unknown

RUN CGO_ENABLED=0 go build -ldflags="-X github.com/piraeusdatastore/piraeus-ha-controller/pkg/consts.Version=${VERSION} -extldflags=-static"  -v ./cmd/...

FROM gcr.io/distroless/static:latest
COPY --from=builder /src/piraeus-ha-controller /piraeus-ha-controller
USER nonroot
ENTRYPOINT ["/piraeus-ha-controller"]
