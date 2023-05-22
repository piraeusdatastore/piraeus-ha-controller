# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.1.4] - 2023-05-22

### Changed

- Wait for initial cache sync before starting the server.

## [1.1.3] - 2023-03-01

### Changed

- Ignore non-running Pods during fail-over events.

## [1.1.2] - 2023-02-20

### Changed

- Build with go 1.19
- Bumped dependencies

## [1.1.1] - 2022-09-20

### Fixed

- No longer attempt to parse numbers from drbdsetup status that are not relevant. This prevents issue when said numbers
  are outside the expected range.

## [1.1.0] - 2022-08-08

### Added
- Exempt Pods that are attached to other types of storage by default, unless the volumes are known to be safe (such as
  ConfigMap, DownwardAPI, Secret, and other readonly volumes).

### Fixed
- Fixed a bug that meant the manual Pod exemption from fail over via annotations would be ignored.

## [1.0.1] - 2022-07-25

### Changed
- Fixed an issue with generated events that would lead to the controller to panic because of a nil interface.
- Immediately delete volume attachment if node is not ready.
- Fixed a concurrent map write when failing over multiple resources at once.

## [1.0.0] - 2022-07-21

### Breaking
- Complete rewrite to work as a node agent instead of relying on LINSTOR. The working principle remains the same, but
  fail-over is no longer (directly) dependent on uninterrupted LINSTOR communications.

### Added
- Force demotion of DRBD resources that are suspended in IO and have pods that should terminate. This enables
  a node to automatically recover from a stuck situation should a network interruption cause DRBD to suspend.
- Add a custom Taint to nodes that have volumes without quorum, preventing replacement pods being scheduled while
  the storage layer is interrupted.

### Changed
- Force deletion of Pods if running Node appears not ready (i.e. it cannot confirm deletion of the Pod).
- Query Kubernetes API about available eviction methods instead of falling back to worse methods on errors.

## [0.3.0] - 2022-02-03

### Added
- Include [`linstor-wait-until`](https://github.com/LINBIT/linstor-wait-until) in docker image. It is used to wait
  for the LINSTOR API to get ready on pod initialization.

## [0.2.0] - 2021-08-31

### Added
- Build for arm64. [#9]

[#9]: https://github.com/piraeusdatastore/piraeus-ha-controller/pull/9

### Changed
- Update golang dependencies to kubernetes 1.22.
- Use `gcr.io/distroless/static` base image and run as non-root user.

## [0.1.3] - 2021-01-14
### Added
- This changelog

### Fixed
- Instruct Kubernetes to send bookmark events for watches. This ensures restarted watches start from a recent
  enough resource version. [#6]
- Fixes the resource filter applied to PVC and VA watches. A bug introduced in 0.1.2 caused the HA Controller to filter
  all resources (Pods, PVCs, VolumeAttachments) based on filters for Pods.

[#6]: https://github.com/piraeusdatastore/piraeus-ha-controller/pull/6

## [0.1.2] - 2021-01-12
### Added
- `--leader-election-resource-name` option added. Used to set the name of the lease resource [#1]

[#1]: https://github.com/piraeusdatastore/piraeus-ha-controller/pull/1

### Fixed
- Kubernetes Watches are now re-tried instead of crashing the whole application in case of timeouts [#5]

[#5]: https://github.com/piraeusdatastore/piraeus-ha-controller/pull/5

## [0.1.1] - 2020-12-16
### Fixed
- Fixed a crash caused by watching PersistentVolumeClaims instead VolumeAttachments

## [0.1.0] - 2020-12-15
### Added
- Initial implementation of HA Controller (watching Kubernetes and LINSTOR events, deleting Pods and VolumeAttachments)
- Deployment example
- README with motivating example

[Unreleased]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v1.1.4...HEAD
[1.1.4]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v1.1.3...v1.1.4
[1.1.3]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v1.1.2...v1.1.3
[1.1.2]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v1.1.1...v1.1.2
[1.1.1]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v0.3.0...v1.0.0
[0.3.0]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v0.1.3...v0.2.0
[0.1.3]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/piraeusdatastore/piraeus-ha-controller/releases/tag/v0.1.0
