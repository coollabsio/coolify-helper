#!/usr/bin/env bash
set -euo pipefail

OPTION="$*"
VERSION="${1:-}"
LINE=v1
CDN_REPOSITORY=coollabsio/coollabs-cdn
VERSIONS_FILE=json/coolify/versions.json
WORK_DIR="$(mktemp -d)"

BOLD=$'\033[1m' BLUE=$'\033[34m' GREEN=$'\033[32m' RED=$'\033[31m' RESET=$'\033[0m'

info() { printf '  %sℹ%s %s\n' "$BLUE" "$RESET" "$*"; }
ok() { printf '  %s✔%s %s\n' "$GREEN" "$RESET" "$*"; }
die() {
	printf '  %s✖%s %s\n' "$RED" "$RESET" "$*" >&2
	exit 1
}

trap 'rm -rf "$WORK_DIR"' EXIT

main() {
	[[ "$OPTION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "'$OPTION' is not an option, expected a stable version such as 1.0.20"
	[[ "$VERSION" == "${LINE#v}".* ]] || die "helper $VERSION is not on the line $LINE, expected a ${LINE#v}.x version, every Coolify instance takes a newer helper.version from the CDN"
	clone_cdn
	write_versions
	commit_versions
	open_pull_request
}

clone_cdn() {
	gh repo clone "$CDN_REPOSITORY" "$WORK_DIR/cdn" -- --quiet --depth 1
	[ -f "$WORK_DIR/cdn/$VERSIONS_FILE" ] || die "$CDN_REPOSITORY has no $VERSIONS_FILE, expected the versions every Coolify instance reads"
	ok "cloned $CDN_REPOSITORY"
}

write_versions() {
	local versions="$WORK_DIR/cdn/$VERSIONS_FILE"
	previous="$(jq -r '.stable.helper.version // empty' "$versions")"
	jq --indent 4 --arg version "$VERSION" '(.stable, .preview, .nightly, .coolify | select(.) | .helper.version) = $version' "$versions" > "$WORK_DIR/versions.json"
	cp "$WORK_DIR/versions.json" "$versions"
	if cdn diff --quiet; then
		ok "$VERSIONS_FILE has the helper version $BOLD$VERSION$RESET already"
		exit 0
	fi
	ok "$VERSIONS_FILE sets the helper version ${previous:+$previous → }$BOLD$VERSION$RESET of the stable, preview and nightly channel and of older instances"
}

commit_versions() {
	title="chore: sync the helper version ${previous:+from v$previous }to v$VERSION"
	branch="sync/helper-v$VERSION"
	cdn commit --quiet --all --message "$title"
	cdn push --quiet --force origin "HEAD:refs/heads/$branch"
	ok "pushed $branch to $CDN_REPOSITORY"
}

open_pull_request() {
	local pull_request superseded
	cat > "$WORK_DIR/body.md" <<- BODY
		Syncs the helper version of every channel to v$VERSION, see its [release notes](https://github.com/coollabsio/coolify-helper/releases/tag/v$VERSION).

		## Changes

		- Sync the helper version ${previous:+from v$previous }to v$VERSION

		> [!IMPORTANT]
		> Squash merge this PR to keep the commit history clean.
	BODY
	pull_request="$(gh pr list --repo "$CDN_REPOSITORY" --head "$branch" --state open --json url --jq '.[0].url // empty')"
	if [ -n "$pull_request" ]; then
		gh pr edit "$pull_request" --repo "$CDN_REPOSITORY" --title "$title" --body-file "$WORK_DIR/body.md" > /dev/null
		ok "updated $pull_request"
	else
		pull_request="$(gh pr create --repo "$CDN_REPOSITORY" --base main --head "$branch" --title "$title" --body-file "$WORK_DIR/body.md")"
		ok "opened $pull_request"
	fi
	gh pr list --repo "$CDN_REPOSITORY" --state open --json headRefName,url \
		| jq -r --arg branch "$branch" '.[] | select((.headRefName | startswith("sync/helper-")) and .headRefName != $branch) | .url' \
		| while read -r superseded; do
			gh pr close "$superseded" --repo "$CDN_REPOSITORY" --delete-branch --comment "Superseded by $pull_request" > /dev/null
			ok "closed $superseded, superseded by $pull_request"
		done
	info "every Coolify instance takes helper $VERSION with its next update check once this pull request is merged and deployed"
}

cdn() {
	git -C "$WORK_DIR/cdn" "$@"
}

main
