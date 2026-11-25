---
type: added
---
`coolify-affected`, which decides which applications of a repository a push affects, so that Coolify deploys only those

- `coolify-affected evaluate` reads the push and the candidate applications as JSON on stdin and prints a deploy or skip decision for each of them, with the reason and the files that matched
- Keeps a blobless mirror of every repository on the `/mirrors` volume, fetches only new objects and diffs trees, so merges, force pushes and empty commits are exact
- Narrows each application by its build context and `.dockerignore`, the `COPY` sources of its Dockerfile, the services of its Compose file, and its package in npm, Yarn, pnpm, Bun, Turborepo and Nx workspaces; anything it cannot read for certain counts as a change
- `coolify-affected scan` finds references of an application to other parts of the repository that no manifest declares, `resolve` prints the resolved sets, and `mirror prune` deletes unused mirrors
- The JSON Schemas of the contract are in `src/affected/schema`, and `src/affected/testdata/matcher-conformance.json` keeps Coolify's PHP matchers identical
