#!/usr/bin/env bash
# Scan Git-visible files, the staged index, or outgoing commits without private working-state mounts.
set -euo pipefail

mode=${1:?usage: check-secrets.sh tree|staged|push <pinned-image> [push-remote]}
image=${2:?a pinned Gitleaks image is required}
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
mapfile -t git_local_env < <(git rev-parse --local-env-vars)

# Hooks export repository-local Git variables. Clear them only for foreign
# repositories; keep them when resolving the real source index and objects.
foreign_command() (
	unset "${git_local_env[@]}"
	"$@"
)

scan_dir=$(mktemp -d /tmp/found-footy-secrets.XXXXXX)
case "$scan_dir" in /tmp/found-footy-secrets.*) ;; *) exit 2 ;; esac
trap 'rm -rf -- "$scan_dir"' EXIT

docker_args=(run --rm --network none --read-only --memory 512m --memory-swap 512m
	--cpus 2 --user "$(id -u):$(id -g)" --cap-drop ALL
	--security-opt no-new-privileges --tmpfs /tmp:rw,noexec,nosuid,size=16m)
scan_args=(--redact=100 --no-banner --no-color --verbose --ignore-gitleaks-allow
	--gitleaks-ignore-path /dev/null --exit-code 10 --timeout 60)

# A minimal bare repository exposes objects and a copied index, not .env,
# cookie files, private recovery bundles, credentials, or workstation Git config.
prepare_git() {
	if [[ $(git rev-parse --is-shallow-repository) == true ]]; then
		printf 'secret scan requires a full clone, not shallow history\n' >&2
		exit 2
	fi
	git_objects=$(git rev-parse --path-format=absolute --git-path objects)
	if [[ -s "$git_objects/info/alternates" ]]; then
		printf 'secret scan requires a self-contained Git object store\n' >&2
		exit 2
	fi
	foreign_command git init --bare --initial-branch=scan --quiet "$scan_dir/git"
	if head=$(git rev-parse --verify HEAD 2>/dev/null); then
		foreign_command env GIT_OBJECT_DIRECTORY="$git_objects" git -C "$scan_dir/git" update-ref refs/heads/scan "$head"
	fi
	docker_args+=(--mount "type=bind,src=$scan_dir/git,dst=/repo,readonly"
		--mount "type=bind,src=$git_objects,dst=/repo/objects,readonly" --workdir /repo)
}

case "$mode" in
	tree)
		mkdir "$scan_dir/tree"
		"$repo_root/scripts/go-scope.sh" snapshot "$scan_dir/tree"
		# Scan a clean parent so project .gitleaks.toml/.gitleaksignore files
		# cannot silently replace the pinned defaults or suppress findings.
		docker "${docker_args[@]}" --mount "type=bind,src=$scan_dir/tree,dst=/scan/tree,readonly" \
			"$image" dir "${scan_args[@]}" /scan
		;;
	staged)
		prepare_git
		index=$(git rev-parse --path-format=absolute --git-path index)
		[[ -f "$index" ]] || { printf 'no staged index to scan\n'; exit 0; }
		cp "$index" "$scan_dir/git/index"
		shared_index=$(git rev-parse --path-format=absolute --shared-index-path)
		if [[ -n "$shared_index" ]]; then
			cp "$shared_index" "$scan_dir/git/$(basename "$shared_index")"
		fi
		docker "${docker_args[@]}" "$image" git --staged "${scan_args[@]}" /repo
		;;
	push)
		includes=()
		excludes=()
		while read -r local_ref local_oid remote_ref remote_oid; do
			[[ -n "$local_oid" ]] || continue
			[[ "$local_oid" =~ ^0+$ ]] && continue # Deleted refs upload no commits.
			commit=$(git rev-parse --verify "$local_oid^{commit}")
			includes+=("$commit")
			if [[ ! "$remote_oid" =~ ^0+$ ]] && old=$(git rev-parse --verify "$remote_oid^{commit}" 2>/dev/null); then
				excludes+=("$old")
			fi
		done
		[[ ${#includes[@]} -gt 0 ]] || { printf 'no outgoing commits to scan\n'; exit 0; }
		remote=${3:-}
		# Exclude only this destination's known history, never another remote's
		# private history. Direct-URL pushes fall back to the advertised old tips.
		if [[ -n "$remote" ]] && git config --get "remote.$remote.url" >/dev/null 2>&1; then
			while read -r oid; do excludes+=("$oid"); done < <(
				git for-each-ref --format='%(objectname)' "refs/remotes/$remote/"
			)
		fi
		revisions=("${includes[@]}")
		[[ ${#excludes[@]} -eq 0 ]] || revisions+=(--not "${excludes[@]}")
		prepare_git
		count=$(git rev-list --count "${revisions[@]}")
		[[ "$count" != 0 ]] || { printf 'no unpublished commits to scan\n'; exit 0; }
		printf 'scanning %s outgoing commits\n' "$count"
		log_opts="--root --full-history --diff-merges=first-parent --no-ext-diff --no-textconv ${revisions[*]}"
		docker "${docker_args[@]}" "$image" git "${scan_args[@]}" --log-opts="$log_opts" /repo
		;;
	*) printf 'unknown secret-scan mode: %s\n' "$mode" >&2; exit 2 ;;
esac
