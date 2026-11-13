---
type: maintenance
---
Replaced the release process, `RELEASE.md` describes it

- Every change has a change file in `.changes/`, which `scripts/release.sh` writes into `CHANGELOG.md`
- `main` is merged forward into `next` and the release branches after every release
- `src/upstream.json` pins every tool with url, size and SHA-256 per platform, which the build verifies, instead of the version arguments and the `SHA256SUMS` block of the Dockerfile
- A stable release opens a pull request on [coollabsio/coollabs-cdn](https://github.com/coollabsio/coollabs-cdn) that sets the helper version, so every Coolify instance takes a new version without a change in Coolify
