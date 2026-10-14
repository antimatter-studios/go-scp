#!/usr/bin/env bash
# Releases the library and the SFTP module together. pkg/sftp requires the library
# version it is released with, both tags go on one commit, and a release is done
# only once the module proxy serves both and a clean program builds against each.
#
# Usage: scripts/release.sh prepare|tag|verify <version> <sftp-version>
#
#   prepare  On a release branch made from main: dates the changelog's Unreleased
#            section as <version>, points pkg/sftp and the SFTP example at the new
#            versions, and commits that for a pull request.
#   tag      On main, once that pull request is merged: tags both modules on HEAD,
#            pushes the tags, publishes the GitHub releases with the changelog's
#            notes, and verifies.
#   verify   Checks a published release as a program that requires it sees it,
#            through the module proxy alone.
#
# For example: scripts/release.sh prepare v0.8.0 v0.3.0
set -euo pipefail
cd "$(dirname "$0")/.."

module=github.com/antimatter-studios/go-scp
sftp_module=$module/pkg/sftp
repo=antimatter-studios/go-scp
proxy=https://proxy.golang.org

fail() {
	echo "release: $*" >&2
	exit 1
}

[ $# -eq 3 ] || fail "usage: scripts/release.sh prepare|tag|verify <version> <sftp-version>"
step=$1 version=$2 sftp_version=$3
for v in "$version" "$sftp_version"; do
	[[ $v =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "$v is not a version such as v1.2.3"
done
sftp_tag=pkg/sftp/$sftp_version

# notes prints the changelog's section for the version, without its heading or
# the blank lines around it.
notes() {
	awk -v heading="## $version" '
		index($0, heading) == 1 && (length($0) == length(heading) || substr($0, length(heading) + 1, 1) == " ") { found = 1; next }
		found && /^## / { exit }
		found { print }
	' CHANGELOG.md | sed -e '/./,$!d' | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}'
}

# required prints the version of the library that the go.mod on stdin requires.
required() {
	awk -v m="$module" '$1 == m && $2 != "=>" { print $2 } $1 == "require" && $2 == m { print $3 }' | head -n 1
}

# clean fails unless the tree has no changes to tracked files.
clean() {
	[ -z "$(git status --porcelain --untracked-files=no)" ] || fail "the tree has uncommitted changes"
}

prepare() {
	[ "$(git branch --show-current)" != main ] || fail "prepare runs on a release branch made from main, not on main"
	clean
	if grep -q '^## Unreleased$' CHANGELOG.md; then
		local dated="## $version — $(date -u +%Y-%m-%d)"
		local alongside="The SFTP module is released alongside as \`$sftp_tag\`, which needs this"
		awk -v dated="$dated" -v alongside="$alongside" '
			!done && $0 == "## Unreleased" { print dated; print ""; print alongside; print "version."; done = 1; next }
			{ print }
		' CHANGELOG.md >CHANGELOG.md.new
		mv CHANGELOG.md.new CHANGELOG.md
	fi
	[ -n "$(notes)" ] || fail "CHANGELOG.md has no Unreleased section and none for $version"

	(cd pkg/sftp && go mod edit -require="$module@$version")
	(cd examples/sftp && go mod edit -require="$module@$version" -require="$sftp_module@$sftp_version")
	(cd pkg/sftp && go build ./...)
	(cd examples/sftp && go build ./...)
	git add CHANGELOG.md pkg/sftp/go.mod examples/sftp/go.mod
	git commit -q -m "Release $version, and pkg/sftp $sftp_version on it" -m "- Date the changelog section $version and note the SFTP module's $sftp_version alongside.
- pkg/sftp requires the library at $version, and the SFTP example requires both at their new versions."
	echo "Committed the release of $version and $sftp_tag; open a pull request for it."
}

tag() {
	[ "$(git branch --show-current)" = main ] || fail "tag runs on main"
	clean
	git fetch -q origin main
	[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || fail "main is not at origin/main; pull first"
	[ "$(required <pkg/sftp/go.mod)" = "$version" ] || fail "pkg/sftp requires the library at $(required <pkg/sftp/go.mod), not $version: run prepare first"
	local body
	body="$(notes)"
	[ -n "$body" ] || fail "CHANGELOG.md has no section for $version"

	git tag -a "$version" -m "$version"
	git tag -a "$sftp_tag" -m "$sftp_tag"
	git push -q origin "$version" "$sftp_tag"
	gh release create "$version" -R "$repo" --verify-tag --title "$version" --notes "$body"
	gh release create "$sftp_tag" -R "$repo" --verify-tag --latest=false --title "pkg/sftp $sftp_version" \
		--notes "The SFTP module, released alongside $version, which it requires. See $version for what changed."
	verify
}

# origin prints the commit the proxy has a module's version at.
origin() {
	curl -sf "$proxy/$1/@v/$2.info" | sed -n 's/.*"Hash":"\([0-9a-f]*\)".*/\1/p'
}

# consumer builds, in work, a new program that uses use from package pkg at
# version at, through the proxy alone, and leaves its go.sum there.
consumer() {
	local work=$1 at=$2 pkg=$3 use=$4
	mkdir -p "$work"
	printf 'package main\n\nimport "%s"\n\nvar _ = %s\n\nfunc main() {}\n' "$pkg" "$use" >"$work/main.go"
	(
		cd "$work"
		{ go mod init example.com/consumer && go get "$pkg@$at" && go mod tidy && go build ./...; } >log 2>&1 || {
			cat log >&2
			exit 1
		}
	)
}

verify() {
	local lib_commit sftp_commit
	lib_commit="$(origin "$module" "$version")"
	sftp_commit="$(origin "$sftp_module" "$sftp_version")"
	[ -n "$lib_commit" ] || fail "the module proxy does not serve $module@$version"
	[ -n "$sftp_commit" ] || fail "the module proxy does not serve $sftp_module@$sftp_version"
	[ "$lib_commit" = "$sftp_commit" ] || fail "$version is at ${lib_commit:0:7} but $sftp_tag at ${sftp_commit:0:7}; both belong on one commit"
	local needs
	needs="$(curl -sf "$proxy/$sftp_module/@v/$sftp_version.mod" | required)"
	[ "$needs" = "$version" ] || fail "$sftp_tag requires the library at ${needs:-nothing}, not $version"

	# Consumers that see nothing of this checkout: their own module cache, the
	# public proxy and checksum database, and no workspace or private settings.
	work="$(mktemp -d)"
	trap 'rm -rf "$work"' EXIT
	export GOMODCACHE="$work/modcache" GOPROXY="$proxy" GOSUMDB=sum.golang.org GOFLAGS=-modcacherw GOWORK=off
	export GOPRIVATE="" GONOPROXY="" GONOSUMDB="" GOTOOLCHAIN=local
	consumer "$work/scp" "$version" "$module/pkg/scp" scp.NewClient || fail "a program using pkg/scp@$version does not build"
	! grep -q 'github.com/pkg/sftp' "$work/scp/go.sum" || fail "a program using only pkg/scp@$version depends on github.com/pkg/sftp"
	consumer "$work/sftp" "$sftp_version" "$sftp_module" sftp.NewClient || fail "a program using $sftp_tag does not build"
	echo "$version and $sftp_tag are released at ${lib_commit:0:7}: the proxy serves both, and programs using each build."
}

case "$step" in
prepare) prepare ;;
tag) tag ;;
verify) verify ;;
*) fail "unknown step $step: prepare, tag or verify" ;;
esac
