---
type: added
---
SBOM and build provenance attestations on every image, the SBOM lists every package of the image and the tools it adds with their versions and licenses

- Every tool declares its name, version, license and CPE in a package note in its binary, as the tools have no package database, so `syft` lists them from the image too
