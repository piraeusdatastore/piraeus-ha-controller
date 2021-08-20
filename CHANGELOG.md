# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v0.1.3...HEAD
[0.1.3]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/piraeusdatastore/piraeus-ha-controller/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/piraeusdatastore/piraeus-ha-controller/releases/tag/v0.1.0
