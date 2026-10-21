#!/usr/bin/env bash
# Exercise the real offline scanner against synthetic secrets in a disposable Git repository.
set -euo pipefail

image=${1:?usage: test-secrets.sh <pinned-image>}
source_root=$(git rev-parse --show-toplevel)
checker=$source_root/scripts/check-secrets.sh
mapfile -t git_local_env < <(git rev-parse --local-env-vars)
unset "${git_local_env[@]}"
test_dir=$(mktemp -d /tmp/found-footy-secret-tests.XXXXXX)
case "$test_dir" in /tmp/found-footy-secret-tests.*) ;; *) exit 2 ;; esac
trap 'rm -rf -- "$test_dir"' EXIT

git init --quiet --initial-branch=main "$test_dir/repo"
cd "$test_dir/repo"
git config user.name 'Found Footy Gate Test'
git config user.email vedanta@local
git config commit.gpgsign false
git config remote.origin.url https://example.invalid/disposable.git
mkdir scripts
cp "$source_root/scripts/go-scope.sh" scripts/
printf '.env\n' > .gitignore
printf 'clean fixture\n' > candidate.txt
git add .
git commit --quiet -m 'test: create clean fixture'
base=$(git rev-parse HEAD)
git update-ref refs/remotes/origin/main "$base"

# Construct an unmistakably synthetic PAT at runtime; never commit its full
# spelling to the application or contact an authenticated service with it.
token=$(printf 'ghp_%s%s%s%s' 'e8ZQm2vN7a' 'K9tR3xW6sD' 'b4YpJ5cF0u' 'H1LqGz')
printf 'api_key="%s"\n' "$token" > .env

# Assertions distinguish a detected leak (10) from scanner/Docker errors.
expect_scan() {
	local expected=$1 label=$2 status=0
	shift 2
	"$checker" "$@" > "$test_dir/output" 2>&1 || status=$?
	if [[ "$status" != "$expected" ]]; then
		printf 'FAIL: %s (exit %s, expected %s)\n' "$label" "$status" "$expected" >&2
		cat "$test_dir/output" >&2
		exit 1
	fi
	if grep -Fq "$token" "$test_dir/output"; then
		printf 'FAIL: scanner output was not fully redacted\n' >&2
		exit 1
	fi
	printf 'PASS: %s\n' "$label"
}

expect_scan 0 'ignored .env is outside the tree snapshot' tree "$image"
odd_path=$'naïve clip\nname.txt'
printf 'api_key="%s"\n' "$token" > "$odd_path"
expect_scan 10 'Unicode and newline filenames cannot disappear from snapshots' tree "$image"
rm -- "$odd_path"
printf 'api_key="%s" # gitleaks:allow\n' "$token" > candidate.txt
git add candidate.txt
git update-index --split-index
printf 'clean unstaged replacement\n' > candidate.txt
GIT_DIR="$PWD/.git" GIT_WORK_TREE="$PWD" GIT_INDEX_FILE="$PWD/.git/index" \
	expect_scan 10 'hook environment and split index preserve the actual staged secret' staged "$image"
expect_scan 0 'working-tree snapshot reads the working version' tree "$image"
printf 'title = "No detector rules"\n' > .gitleaks.toml
printf 'tree/candidate.txt:github-pat:1\ncandidate.txt:github-pat:1\n' > .gitleaksignore
printf 'api_key="%s" # gitleaks:allow\n' "$token" > candidate.txt
expect_scan 10 'project config and ignore files cannot disable the tree detector' tree "$image"
printf 'clean unstaged replacement\n' > candidate.txt
git commit --quiet -m 'test: retain synthetic secret for history regression'
secret_commit=$(git rev-parse HEAD)
git add candidate.txt
git commit --quiet -m 'test: remove synthetic secret'
head=$(git rev-parse HEAD)
expect_scan 0 'clean staged removal is allowed' staged "$image"
git config remote.private.url https://example.invalid/private.git
git update-ref refs/remotes/private/main "$head"
printf 'refs/heads/main %s refs/heads/main %s\n' "$head" "$base" | \
	expect_scan 10 'push catches a secret removed by a later commit' push "$image" origin
printf 'refs/heads/other %s refs/heads/other %040d\n' "$secret_commit" 0 | \
	expect_scan 10 'new non-checked-out branch is scanned' push "$image" origin
printf 'refs/heads/main %040d refs/heads/main %s\n' 0 "$head" | \
	expect_scan 0 'ref deletion does not scan historical debt' push "$image" origin
git update-ref refs/remotes/origin/main "$head"
printf 'refs/heads/main %s refs/heads/main %s\n' "$head" "$head" | \
	expect_scan 0 'already-published history is not a permanent push blocker' push "$image" origin

git checkout --quiet -b side
printf 'side change\n' > side.txt
git add side.txt
git commit --quiet -m 'test: create side branch'
git checkout --quiet main
printf 'main change\n' > main.txt
git add main.txt
git commit --quiet -m 'test: diverge main'
git merge --quiet --no-commit --no-ff side > "$test_dir/merge-output" 2>&1
printf 'api_key="%s"\n' "$token" > merge-only.txt
git add merge-only.txt
git commit --quiet -m 'test: introduce synthetic secret only in merge resolution'
merged=$(git rev-parse HEAD)
printf 'refs/heads/main %s refs/heads/main %s\n' "$merged" "$head" | \
	expect_scan 10 'merge-resolution-only secrets are scanned' push "$image" origin

git clone --quiet --depth 1 "file://$PWD" "$test_dir/shallow"
(
	cd "$test_dir/shallow"
	printf 'refs/heads/main %s refs/heads/main %s\n' "$merged" "$merged" | \
		expect_scan 2 'shallow history fails closed before a zero-count shortcut' push "$image" origin
)
git clone --quiet --shared "$PWD" "$test_dir/shared"
(
	cd "$test_dir/shared"
	printf 'refs/heads/main %s refs/heads/main %s\n' "$merged" "$merged" | \
		expect_scan 2 'external object stores fail closed before a zero-count shortcut' push "$image" origin
)
git init --quiet "$test_dir/repo/nested"
printf 'private nested fixture\n' > nested/README.md
printf 'api_key="%s"\n' "$token" > nested/.env
expect_scan 1 'nested repositories cannot recursively expose ignored files' tree "$image"
printf 'secret-scanning acceptance passed\n'
