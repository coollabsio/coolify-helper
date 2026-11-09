---
type: added
---
Reproducible builds, the image and every file in it carry the date of their commit, so a rebuild of a commit with the same Alpine packages gives the same image digest

- The `org.opencontainers.image.created` label is the date of the commit
