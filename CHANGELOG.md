# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security

- Lock every action and the actions it calls to a commit in `.github/workflows/actions.lock`, together with the IDs of its owner and repository, which a commit SHA alone does not check

### Added

- Publish prereleases from `next` and the release branches, tagged like `1.1.0-beta.1`, and the moving `preview` tag
- Publish the image of every pull request from a branch of this repository as `ghcr.io/coollabsio/coolify-helper:pr-<number>`, so a change can be tried before it is merged

### Changed

- Move the image from [coollabsio/coolify](https://github.com/coollabsio/coolify) to this repository, which releases it with git tags and GitHub releases
- Replace the release process, `RELEASE.md` describes it
- Merge `main` forward into `next` and the release branches after every release
- Publish the image as an OCI index instead of a Docker manifest list

### Removed

- Remove the `next` and `<version>-next` image tags, prereleases and the moving `preview` tag take their place
- Remove the image tag of every platform, such as `1.0.19-amd64`, the version tag picks the platform
