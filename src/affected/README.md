# coolify-affected

`coolify-affected` decides which applications of a Git repository a push affects, so that Coolify deploys only those. It is a static Go binary in the helper image at `/usr/local/bin/coolify-affected`.

The rule it never breaks: **widen, never narrow**. A wrong deploy costs a build, a wrong skip ships stale code. Whenever it can't be certain, a limit or resolver is dropped (the set gets larger) and the reason is recorded in `widened`; whenever the diff is unknown, the application deploys.

## Commands

| Command | Input | Output |
| --- | --- | --- |
| `evaluate` | [`schema/evaluate-request.json`](schema/evaluate-request.json) on stdin | [`schema/evaluate-response.json`](schema/evaluate-response.json) |
| `scan` | [`schema/scan-request.json`](schema/scan-request.json) on stdin | [`schema/scan-response.json`](schema/scan-response.json) |
| `resolve` | [`schema/resolve-request.json`](schema/resolve-request.json) on stdin | [`schema/resolve-response.json`](schema/resolve-response.json) |
| `mirror prune [--days 14] [--max-total-bytes 53687091200]` | flags | [`schema/mirror-prune-response.json`](schema/mirror-prune-response.json) |
| `version [--json]` | | [`schema/version.json`](schema/version.json) |

`evaluate`, `resolve` and `mirror prune` take `--mirrors DIR` (default `/mirrors`, or `$COOLIFY_AFFECTED_MIRRORS`). `--days 0` turns pruning by age off.

- stdout carries exactly one JSON document. Logs go to stderr and never contain credentials or file contents.
- Exit code `0`: a valid response. Per-application problems are inside it, and those applications deploy.
- Exit code `2`: invalid input. Exit code `3`: fatal failure. Both print [`schema/error.json`](schema/error.json) on stdout.
- `evaluate` answers within `limits.timeout_seconds`: every application that is not decided by then deploys with `timeout`, and the exit code is `0`.

The schemas are JSON Schema 2020-12 and use `$id`s under `https://coolify.io/schemas/coolify-affected/v1/`, with shared definitions in [`schema/common.json`](schema/common.json). Every response the tests produce is validated against them. The version of the contract is `schema_version` 1; a breaking change bumps it.

`tool_version` and `version` are the version of the helper image that built the binary.

## How `evaluate` decides

For each application, in order:

1. `is_fork`: `deploy`, `fork_no_narrowing`. Nothing is fetched or read.
2. No `base_commit` and no `merge_base_ref`: `deploy`, `no_base`.
3. The diff of its base is not `ok`: `deploy` with `base_unreachable`, `too_many_changes` or `error`.
4. The base has the same tree as `after` (`git commit --allow-empty`): `deploy`, `empty_diff`.
5. `watch_paths` mode: Coolify's legacy matcher. Blank watch paths deploy on every change, as in Coolify's webhooks.
6. `smart` mode: the trigger set. A changed file matches when every limit includes it. `matched`, or `resolution_failed` when only the build context still narrows because the other limits widened, or `no_match`.

### Mirror and diff

- One bare, blobless partial clone per `mirror_key` at `/mirrors/<mirror_key>.git`, with full commit and tree history, created in a temporary directory and renamed into place after its first successful fetch.
- An exclusive `flock` on `/mirrors/<mirror_key>.lock`, waited for within the timeout. Different keys run concurrently.
- `git fetch --filter=blob:none --no-tags --prune origin +<ref>:refs/coolify/<ref without refs/>`. If fetching every ref at once fails (a deleted branch), each ref is fetched alone. Missing commits (`after`, bases) are fetched by object name.
- A watchdog kills a fetch that grows the mirror past `max_mirror_bytes`; the mirror is then removed and every application deploys with `error`.
- One `git diff-tree -r -z --no-renames --name-only <base> <after>` per distinct base. It compares trees, so merges, force pushes, rewinds and rewrites are exact, a move reports both paths, and gitlinks (submodule pointers) are included. `relation` (`git merge-base --is-ancestor`) is information only.
- New previews: `base = merge-base(after, merge_base_ref)` after fetching that ref.

### Reading files without a checkout

The tree of `after` comes from `git ls-tree -r -z` (trees are local in a blobless clone). Before any parsing, every blob the run may need is fetched in **one round trip**: the manifests of every resolver, and each application's Dockerfile, ignore files and Compose file. The fetch is git's own promisor fetch, `git -c fetch.negotiationAlgorithm=noop fetch --filter=blob:none --stdin origin` with the object names on stdin. Reads go through one `git cat-file --batch` process, with `GIT_NO_LAZY_FETCH=1` so a missing blob is never fetched one by one, and with `max_manifest_files` and `max_manifest_bytes` caps: past a cap, the limit or resolver that needed the read widens.

Measured on a blobless mirror of a repository with 50,000 files, fetching its 500 `package.json` blobs from a local remote:

| Approach | Time | Result |
| --- | --- | --- |
| `fetch --stdin` with the 500 object names (used) | 168 ms | 1 round trip, pack of 1.3 MB |
| One fetch per object | ~12 ms per object, so ~6 s for 500 | 500 round trips |
| `git backfill` | 3,162 ms | every blob of the history, pack of 4.2 MB |
| `git backfill --sparse` | | needs a sparse-checkout, which a bare mirror can't have |

Over a network the round trips dominate, so the one-round-trip approach wins by more.

### Graph cache

`manifest_fingerprint` is sha256 over the sorted `(path, blob id, mode)` of every file the resolution read; a file that is absent counts too, so that its appearance changes the fingerprint. The workspace graph is cached at `/mirrors/<mirror_key>.cache/<sha256(tool version, fingerprint)>.json` (at most 64 entries and 64 MiB, least recently used first). A push that touches no manifest reuses the graph: no blob is fetched and no manifest is parsed.

## The trigger set

The compiled set (`trigger_set.compiled`) is what Coolify's PHP fast path matches too: `all_of` rulesets, each with a default and ordered `dir`/`file`/`glob` rules where the last match wins. [`testdata/matcher-conformance.json`](testdata/matcher-conformance.json) has 326 compiled and 237 watch-path cases, and [`testdata/php/compiled_matcher.php`](testdata/php/compiled_matcher.php) is a dependency-free PHP implementation that agrees with the Go matcher on all of them and on 300,000 random sets.

| Limit | Applies to | Is |
| --- | --- | --- |
| `build_context` | every build pack | the base directory; for Dockerfile apps minus what `<Dockerfile>.dockerignore` (or `.dockerignore`) excludes, with BuildKit's `patternmatcher` semantics |
| `dockerfile` | Dockerfile apps | the `COPY`/`ADD` sources and `RUN --mount=type=bind` sources from the context, in every stage, plus the Dockerfile and ignore files |
| `compose` | Compose apps | per service: its context narrowed by its Dockerfile and `.dockerignore`, `additional_contexts`, bind mounts, `env_file`, `configs`/`secrets` files, and every Compose file that is included or extended |
| `workspace` | repositories with a workspace | the anchor package, its workspace dependencies followed transitively, global files and `reference_paths`, inside a "default include" frame |

`managed_config_paths` are included by every limit. Symlinks that a limit includes add their targets (BuildKit follows symlinks in `COPY` sources).

### Resolvers in this version

npm, Yarn (classic and Berry), pnpm and Bun workspaces (with `overrides`/`resolutions` that point at members, and member scripts that build other members as edges); Turborepo (`globalDependencies`, `pkg#task` edges, `$TURBO_ROOT$` inputs); Nx (`project.json`, `implicitDependencies`, `namedInputs`/`sharedGlobals`, TypeScript path aliases of the root and of each project, through `extends`, as edges). Bazel, Pants and Buck are detected and not modeled: the workspace limit then does not apply. The other ecosystems of the brief come in Phases 2 and 3 behind the same `resolver.Resolver` interface, without changes to the core.

Lockfile scoping comes in Phase 2. Until then a changed lockfile matches every application whose limits include it, with `lockfile.affects_app: true`.

### `scan`

`scan` runs in a deployment's checkout and walks the closure's tracked files (in parallel, skipping binary files and files over `max_file_bytes`) for references to parts of the repository that no manifest declares: relative paths in strings, path-like strings resolved from the repository root, imports of workspace packages that are not dependencies, TypeScript `compilerOptions.paths` aliases (folded into `scan`, as the brief allows), and symlinks. A reference into another workspace package adds that package and its dependencies to the closure, and their files are scanned too. An application without an anchor gets an empty, valid result: references could not change its set.

## Security

- Repository content is never executed: no package managers, hooks (`core.hooksPath=/dev/null`), filters, textconv, submodule updates or LFS fetches, and fork content is never read.
- git runs with an empty environment besides `PATH` and the request's `git_env`, no system or global config, no prompts, `GIT_ALLOW_PROTOCOL=https:http:ssh`, and `protocol.allow=never` with `https`, `http` and `ssh` allowed. The tool's settings are applied after the request's, so they always win.
- The request's `git_config` may only set `url.<base>.insteadOf`/`pushInsteadOf` and `http.*` keys (except those that name files); anything else, such as `hook.*` or `credential.helper`, could make git run a program. `git_env` may not set the askpass, editor, pager, config or directory variables. Repository URLs may not carry a password, or any user info for http(s): the URL is stored in the mirror's config on the shared volume.
- `file://` is only allowed in test builds (`-tags coolifytest`), through a constant that production builds can't change.
- Credentials (the request's `git_config` values and user info, sensitive `git_env` values, URL user info, `Authorization` headers and token-shaped strings) are scrubbed from every error, including git's stderr.
- Every path from a request or from repository content is cleaned and kept inside the repository; `mirror_key` must be 64 hex characters. Every read has a size cap; YAML alias expansion, `include`/`extends` depth and symlink chains are capped; Compose files are loaded from a private copy that compose-go can't leave.

## Deviations from the brief

- **Release:** the helper version is not bumped in `release.json`, because a push that changes it releases. The change is described in `.changes/`, and `scripts/release.sh` bumps the version, as `RELEASE.md` describes.
- **`tool_version`** is the helper image version (`IMAGE_VERSION`), not a separate version.
- **`.dockerignore` patterns `.` and `/`** exclude nothing in BuildKit (fsutil never matches the context root), so they compile to no rule instead of excluding the context.
- **`patternmatcher` bug:** it leaves `{`, `|` and `}` unescaped in its regular expressions. Wildcard ignore patterns with those characters make the ignore rules widen.
- **"A glob at the root"** in a `COPY` source means a source whose first segment is only wildcards (`*`, `**/x`). `COPY package*.json ./` compiles exactly and does not widen.
- **Heredocs:** a `COPY`/`ADD` heredoc widens; `RUN` heredocs don't, as `RUN` can't read the context without a bind mount, which is handled.
- **Compose and the build context:** when the Compose file can't be resolved, the `build_context` limit is dropped too, because Compose files can reference paths outside the base directory.
- **`anchors`:** an application can build several packages, so `trigger_set.anchors` lists all of them; `anchor` is the first.
- **Blank `watch_paths`** deploy on every change, as Coolify's webhooks do.
- **Workspace roots** are detected at the repository root only. A workspace in a subdirectory of a repository is not modeled: the base directory still narrows.
- **Commands are read strictly.** The anchor from commands, and the commands and Dockerfile `RUN` lines next to a Dockerfile, Compose or base-directory anchor, are only trusted when every simple command is understood: a package manager or task runner with a filter, an install, or an inert command (`echo`, `mkdir`, `apk add`…). A command whose effect is unknown, such as `node scripts/build.mjs`, `cp`, `python`, `eval` or a root script, means no anchor, and the workspace limit does not apply. This costs the workspace limit for start commands like `node dist/server.js`; the other limits still narrow. The scripts of every member's `package.json` that name other members are edges.
- **Members nested in a member** of the closure, and the dependencies of members with install scripts, are never excluded.
- **ONBUILD triggers of a base image** can't be known without the image. A base image whose name contains `onbuild` makes the dockerfile limit widen; another base image with ONBUILD triggers is a known gap.
- **`BUILDKIT_SYNTAX`** as a Compose build argument selects a custom frontend, which can read anything: the compose limit widens. A Dockerfile app's build arguments come from Coolify and are not known to the tool.
- **Ignore files and Dockerfiles that are symlinks**, and repositories with a newline in a path, make the `.dockerignore` rules widen. Both ignore candidates, and the lowercase `dockerfile` BuildKit falls back to, are always in the set. A base directory that is a symlink, or under one, makes the build context widen.

## Development

There is no Go on the host requirement: [`scripts/test-affected.sh`](../../scripts/test-affected.sh) runs everything in the Go image of `src/Dockerfile`.

```sh
scripts/test-affected.sh           # gofmt, go vet, go test -race with the golden fixtures, build
scripts/test-affected.sh --fuzz    # and every fuzz target, FUZZTIME=10s each by default
scripts/test-affected.sh --bench   # and the benchmarks
```

- Golden fixtures (24) are in [`testdata/golden`](testdata/golden): readable files that build real Git repositories (see `internal/testrepo`), with expected responses in [`testdata/expected`](testdata/expected). Regenerate them with `go test -tags coolifytest ./internal/evaluate -run TestGolden -update`, then review the diff.
- Regenerate the conformance fixture with `go test ./internal/match -run TestConformanceFixture -update` (needs `php`).
- `TestPerformanceTargets` (`go test -tags coolifytest ./internal/evaluate -run TestPerformanceTargets -v`) and `TestScanPerformance` log the numbers below.

### Performance

Synthetic pnpm repository: 50,000 files, 500 members, 100 applications, 2,000 changed files; local remote, Ryzen 7 3700X.

| Target | Measured | Target |
| --- | --- | --- |
| Warm mirror, 100 apps, 2,000 changed files, graph cache cleared | 0.5 s without the fetch (0.72 s with it) | 3 s |
| Warm mirror, no manifest change | 0.51 s, graph from the cache, no blob fetch | 1 s |
| Matching, 100 apps × 50,000 files | 0.24 s on one core | 0.5 s |
| `scan` of a 50,000-file closure on 2 CPUs | 1.45 s | 10 s |
| Peak heap of `evaluate` | 89 MB | 512 MB |
| First fetch of the repository | 0.87 s from a local remote; bounded by `max_mirror_bytes` and the timeout | |

A first fetch transfers every commit and tree of the history but no blob besides the manifests. For a real repository its cost is roughly the size of `git clone --filter=blob:none --bare`, usually a fraction of a full clone; `max_mirror_bytes` caps it.
