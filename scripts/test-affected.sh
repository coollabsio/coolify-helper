#!/usr/bin/env bash
set -euo pipefail

OPTION="$*"
FUZZTIME="${FUZZTIME:-10s}"
MODULE=src/affected

BOLD=$'\033[1m' GREEN=$'\033[32m' RED=$'\033[31m' RESET=$'\033[0m'

heading() { printf '\n  %s%s%s\n' "$BOLD" "$*" "$RESET"; }
ok() { printf '  %s✔%s %s\n' "$GREEN" "$RESET" "$*"; }
die() {
	printf '  %s✖%s %s\n' "$RED" "$RESET" "$*" >&2
	exit 1
}

cd "$(dirname "$0")/.."

main() {
	local size
	[[ "$OPTION" =~ ^(--bench|--fuzz)?$ ]] || die "'$OPTION' is not an option, expected --bench, --fuzz or none"
	command -v docker > /dev/null || die "docker is not installed"
	TEST_IMAGE="$(test_image)"
	heading "Testing coolify-affected with $(go_image)"
	# shellcheck disable=SC2016 # the command runs in the container
	go_run 'test -z "$(gofmt -l .)" || { gofmt -l . >&2; exit 1; }' || die "gofmt lists files that are not formatted"
	ok "every Go file is formatted"
	go_run 'go vet ./... && go vet -tags coolifytest ./...' || die "go vet failed"
	ok "go vet passed"
	go_run 'CGO_ENABLED=1 go test -race -count=1 -tags coolifytest ./...' || die "the tests failed"
	ok "the tests passed, with the race detector and the golden fixtures"
	size="$(go_run 'CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /tmp/coolify-affected ./cmd/coolify-affected && wc -c < /tmp/coolify-affected')" || die "the build failed"
	ok "the static binary has $size bytes"
	if [ "$OPTION" = --fuzz ]; then
		fuzz
	fi
	if [ "$OPTION" = --bench ]; then
		heading "Benchmarks"
		go_run 'go test -run "^$" -bench . -benchmem -tags coolifytest ./...'
	fi
	heading "All coolify-affected tests passed"
}

fuzz() {
	local target package
	heading "Fuzzing every target for $FUZZTIME"
	local log
	log="$(mktemp)"
	while read -r package target; do
		# Read-write, so that a failing input is saved under testdata/fuzz as a reproducer.
		if ! MOUNT=rw go_run "go test -run '^\$' -fuzz '^$target\$' -fuzztime '$FUZZTIME' $package" > "$log" 2>&1; then
			cat "$log" >&2
			rm -f "$log"
			die "$target in $package failed, the failing input is in $MODULE/${package#./}/testdata/fuzz/$target"
		fi
		ok "$target"
	done < <(fuzz_targets)
	rm -f "$log"
}

# fuzz_targets prints "./package FuzzName" for every fuzz target of the module.
fuzz_targets() {
	local file
	grep -rlE '^func Fuzz' --include='*_test.go' "$MODULE" | sort | while read -r file; do
		grep -oE '^func Fuzz[A-Za-z0-9_]+' "$file" | cut -d ' ' -f 2 | sed "s|^|./$(dirname "${file#"$MODULE"/}") |"
	done
}

go_image() {
	sed -n 's/^ARG GO_IMAGE=//p' src/Dockerfile
}

# test_image builds the Go image of src/Dockerfile with git (golden fixtures), gcc (race detector) and php (parity of the matchers).
test_image() {
	if [ -z "${TEST_IMAGE:-}" ]; then
		TEST_IMAGE="coolify-affected-test:$(go_image | sed 's/.*sha256://' | cut -c 1-12)"
		printf 'FROM %s\nRUN apk add --no-cache git gcc musl-dev php84 && ln -s /usr/bin/php84 /usr/local/bin/php\n' "$(go_image)" \
			| docker build --quiet --tag "$TEST_IMAGE" - > /dev/null || die "could not build the test image"
	fi
	echo "$TEST_IMAGE"
}

go_run() {
	docker run --rm -e GOFLAGS=-buildvcs=false -v coolify-affected-go-mod:/go/pkg/mod -v coolify-affected-go-build:/root/.cache/go-build \
		-v "$PWD/$MODULE:/src:${MOUNT:-ro}" -w /src --entrypoint sh "$TEST_IMAGE" -c "$1"
}

main
