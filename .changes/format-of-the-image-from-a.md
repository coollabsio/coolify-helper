---
type: changed
---
Format of the image from a Docker manifest list to an OCI index, the open standard that holds what a Docker manifest list cannot

- The index holds the SBOM and build provenance attestations next to the image of each platform
- The index carries annotations, ghcr.io reads the description and the license of the image from them
- The layers are compressed with zstd, a media type that only OCI defines
