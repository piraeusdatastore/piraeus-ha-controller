# Contributing to Piraeus High Availability Controller

The [Piraeus contributing guide](https://github.com/piraeusdatastore/piraeus/blob/master/CONTRIBUTING.md) describes how to report issues, write commits and changelog entries, and open pull requests.
The sections that follow describe what is specific to this repository.

## Target branch

Open pull requests against the `main` branch.

## Build and test

Install [Go](https://go.dev/) in the version declared in `go.mod`.
Run the unit tests and the linter:

```
$ go test ./...
$ golangci-lint run
```

On every pull request, CI runs `go test ./...`, [`golangci-lint`](https://golangci-lint.run/), and a build of the container image.

## Deployment manifests

Two projects deploy the HA Controller:

* [Piraeus Operator](https://github.com/piraeusdatastore/piraeus-operator), with the manifests in `pkg/resources/cluster/ha-controller/`.
* The `piraeus-ha-controller` chart in [helm-charts](https://github.com/piraeusdatastore/helm-charts).

If you add a command-line option that users must set, or if the HA Controller requires new RBAC permissions, open pull requests in both projects.

## Testing on a cluster

To try a change on a cluster, build an image and push it to your registry:

```
$ make upload REGISTRY=registry.example.com/myname TAG=dev
```

By default, the image is built for `linux/amd64` and `linux/arm64`.
To build for a single platform, set `PLATFORMS`, for example `PLATFORMS=linux/amd64`.
The target also tags the image as `latest`.

If you deploy with Piraeus Operator, set the `ha-controller` component in the [image configuration](https://piraeus.io/docs/stable/explanation/image-configuration/) to the new image.
