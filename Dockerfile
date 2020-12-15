FROM golang:1.15 as builder

WORKDIR /src/
COPY go.* /src/
# Cache mod downloads
RUN go mod download -x

COPY cmd /src/cmd
COPY pkg /src/pkg

ARG GOOS=linux
ARG GOARCH=amd64
ARG SEMVER=0.0.0-0.unknown

RUN CGO_ENABLED=0 go build -ldflags="-X github.com/piraeusdatastore/piraeus-ha-controller/cmd/piraeus-ha-controller.Version=${SEMVER} -extldflags=-static"  -v ./cmd/...

FROM scratch
COPY --from=builder /src/piraeus-ha-controller /piraeus-ha-controller
ENTRYPOINT ["/piraeus-ha-controller"]
