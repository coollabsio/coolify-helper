---
type: maintenance
---
Replaced the release process, `RELEASE.md` describes it

- Every change has a change file in `.changes/`, which `scripts/release.sh` writes into `CHANGELOG.md`
- `main` is merged forward into `next` and the release branches after every release
