#!/bin/sh
# go-scope.sh derives Go modules, packages, and files from Git-visible source.
set -eu

usage() {
	printf 'usage: %s modules | files | packages <module> | snapshot <directory>\n' "$0" >&2
	exit 2
}

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
cd "$repo_root"

git_repo() {
	git -c safe.directory="$repo_root" "$@"
}

modules() {
	git_repo ls-files -co --exclude-standard -- 'go.mod' ':(glob)**/go.mod' |
		sed -e 's|/go\.mod$||' -e 's|^go\.mod$|.|' |
		LC_ALL=C sort -u
}

files() {
	git_repo ls-files -co --exclude-standard -- '*.go' | LC_ALL=C sort -u
}

packages() {
	module=$1
	case "$module" in
		.) prefix= ;;
		*) prefix=$module/ ;;
	esac

	nested_modules=$(modules | while IFS= read -r candidate; do
		[ "$candidate" = "." ] && continue
		[ "$candidate" = "$module" ] && continue
		case "$candidate/" in
			"$prefix"*) printf '%s\n' "$candidate" ;;
		esac
	done)

	files | while IFS= read -r file; do
		case "$file" in
			"$prefix"*) ;;
			*) continue ;;
		esac

		inside_nested=false
		for nested in $nested_modules; do
			case "$file" in
				"$nested/"*) inside_nested=true; break ;;
			esac
		done
		$inside_nested && continue

		relative=${file#"$prefix"}
		directory=$(dirname "$relative")
		if [ "$directory" = "." ]; then
			printf '.\n'
		else
			printf './%s\n' "$directory"
		fi
	done | LC_ALL=C sort -u
}

snapshot() {
	destination=$1
	[ -d "$destination" ] || {
		printf 'snapshot destination is not a directory: %s\n' "$destination" >&2
		exit 1
	}

	git_repo ls-files -co --exclude-standard | while IFS= read -r path; do
		if [ -e "$path" ] || [ -L "$path" ]; then
			cp -a --parents "$path" "$destination"
		fi
	done
}

case ${1:-} in
	modules)
		[ "$#" -eq 1 ] || usage
		modules
		;;
	files)
		[ "$#" -eq 1 ] || usage
		files
		;;
	packages)
		[ "$#" -eq 2 ] || usage
		packages "$2"
		;;
	snapshot)
		[ "$#" -eq 2 ] || usage
		snapshot "$2"
		;;
	*)
		usage
		;;
esac
