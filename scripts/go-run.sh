#!/bin/sh
# go-run.sh lets Go filter Git-visible directories through its active build context.
set -eu

mode=${1:-}
[ -n "$mode" ] || {
	printf 'usage: %s <build|vet|lint|test|test-short|test-live|test-race> <package-dir>...\n' "$0" >&2
	exit 2
}
shift
[ "$#" -gt 0 ] || {
	printf 'no candidate package directories supplied\n' >&2
	exit 1
}

case "$mode" in
	build)
		template='{{if or .GoFiles .CgoFiles}}{{.ImportPath}}{{end}}'
		eligible=$(go list -e -f "$template" "$@")
		;;
	test-live)
		template='{{if or .GoFiles .CgoFiles .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}'
		eligible=$(go list -e -tags live -f "$template" "$@")
		;;
	lint)
		template='{{if or .GoFiles .CgoFiles .TestGoFiles .XTestGoFiles}}{{.Dir}}{{end}}'
		eligible=$(go list -e -f "$template" "$@")
		;;
	vet|test|test-short|test-race)
		template='{{if or .GoFiles .CgoFiles .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}'
		eligible=$(go list -e -f "$template" "$@")
		;;
	*)
		printf 'unknown Go gate mode: %s\n' "$mode" >&2
		exit 2
		;;
esac

[ -n "$eligible" ] || {
	printf 'no packages eligible for Go gate mode %s\n' "$mode" >&2
	exit 1
}

case "$mode" in
	build) exec go build -buildvcs=false $eligible ;;
	vet) exec go vet $eligible ;;
	lint) exec golangci-lint run --timeout 5m $eligible ;;
	test) exec go test -buildvcs=false $eligible ;;
	test-short) exec go test -buildvcs=false -short $eligible ;;
	test-live) exec go test -buildvcs=false -tags live $eligible ;;
	test-race) exec go test -buildvcs=false -race $eligible ;;
esac
