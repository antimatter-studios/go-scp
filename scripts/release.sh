#!/usr/bin/env bash
# Releases both modules, pkg/scp and pkg/sftp, at one version: their tags,
# pkg/scp/<version> and pkg/sftp/<version>, go on one commit, pkg/sftp requires
# pkg/scp at that same version, and a release is done only once it has its GitHub
# release, the module proxy serves both modules, and a clean program builds
# against each.
#
# Usage: scripts/release.sh prepare|tag|verify <version>
#
#   prepare  On a release branch made from main: dates the changelog's Unreleased
#            section as <version>, writes the README's Changelog section from it,
#            points every module that requires another at <version>, and commits
#            that for a pull request.
#   tag      On main, once that pull request is merged: tags both modules on HEAD
#            and pushes the tags. The Release workflow then publishes the GitHub
#            release, with the changelog's notes.
#   verify   Checks a published release: its GitHub release, and the modules as a
#            program that requires them sees them, through the module proxy alone.
#
# For example: scripts/release.sh prepare v0.9.0
set -euo pipefail
cd "$(dirname "$0")/.."

scp_module=github.com/antimatter-studios/go-scp/pkg/scp
sftp_module=github.com/antimatter-studios/go-scp/pkg/sftp
repo=antimatter-studios/go-scp
proxy=https://proxy.golang.org

fail() {
	echo "release: $*" >&2
	exit 1
}

[ $# -eq 2 ] || fail "usage: scripts/release.sh prepare|tag|verify <version>"
step=$1 version=$2
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "$version is not a version such as v1.2.3"
scp_tag=pkg/scp/$version
sftp_tag=pkg/sftp/$version

# documented fails unless CHANGELOG.md has a section for the version with
# something in it. The Release workflow writes the release notes from it.
documented() {
	awk -v heading="## $version" '
		index($0, heading) == 1 && (length($0) == length(heading) || substr($0, length(heading) + 1, 1) == " ") { found = 1; next }
		found && /^## / { exit }
		found && /[^[:space:]]/ { written = 1 }
		END { exit !written }
	' CHANGELOG.md || fail "CHANGELOG.md has no section for $version"
}

# required prints the version of pkg/scp that the go.mod on stdin requires.
required() {
	awk -v m="$scp_module" '$1 == m && $2 != "=>" { print $2 } $1 == "require" && $2 == m { print $3 }' | head -n 1
}

# clean fails unless the tree has no changes to tracked files.
clean() {
	[ -z "$(git status --porcelain --untracked-files=no)" ] || fail "the tree has uncommitted changes"
}

prepare() {
	[ "$(git branch --show-current)" != main ] || fail "prepare runs on a release branch made from main, not on main"
	clean
	if grep -q '^## Unreleased$' CHANGELOG.md; then
		local today dated
		today="$(date -u +%Y-%m-%d)"
		dated="## $version — $today"
		local alongside="The SFTP module is released alongside as \`$sftp_tag\`, which needs this"
		awk -v dated="$dated" -v alongside="$alongside" '
			!done && $0 == "## Unreleased" { print dated; print ""; print alongside; print "version."; done = 1; next }
			{ print }
		' CHANGELOG.md >CHANGELOG.md.new
		mv CHANGELOG.md.new CHANGELOG.md
	fi
	documented
	scripts/changelog-readme.sh

	(cd pkg/sftp && go mod edit -require="$scp_module@$version")
	(cd examples && go mod edit -require="$scp_module@$version" -require="$sftp_module@$version")
	go build ./pkg/sftp/... ./examples/...
	git add CHANGELOG.md README.md pkg/sftp/go.mod examples/go.mod
	git commit -q -m "Release $version" -m "- Date the changelog section $version, and write the README's Changelog section from it.
- pkg/sftp requires pkg/scp at $version, and the examples require both."
	echo "Committed the release of $version; open a pull request for it."
}

tag() {
	[ "$(git branch --show-current)" = main ] || fail "tag runs on main"
	clean
	git fetch -q origin main
	[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || fail "main is not at origin/main; pull first"
	[ "$(required <pkg/sftp/go.mod)" = "$version" ] || fail "pkg/sftp requires pkg/scp at $(required <pkg/sftp/go.mod), not $version: run prepare first"
	documented

	git tag -a "$scp_tag" -m "$scp_tag"
	git tag -a "$sftp_tag" -m "$sftp_tag"
	git push -q origin "$scp_tag" "$sftp_tag"
	echo "Pushed $scp_tag and $sftp_tag. The Release workflow publishes the GitHub release $version;"
	echo "once it has: scripts/release.sh verify $version"
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
	gh release view "$scp_tag" --repo "$repo" --json body --jq .body | grep -q . ||
		fail "$version has no GitHub release with notes; the Release workflow publishes one when $scp_tag is pushed"

	local scp_commit sftp_commit
	scp_commit="$(origin "$scp_module" "$version")"
	sftp_commit="$(origin "$sftp_module" "$version")"
	[ -n "$scp_commit" ] || fail "the module proxy does not serve $scp_module@$version"
	[ -n "$sftp_commit" ] || fail "the module proxy does not serve $sftp_module@$version"
	[ "$scp_commit" = "$sftp_commit" ] || fail "$scp_tag is at ${scp_commit:0:7} but $sftp_tag at ${sftp_commit:0:7}; both belong on one commit"
	local needs
	needs="$(curl -sf "$proxy/$sftp_module/@v/$version.mod" | required)"
	[ "$needs" = "$version" ] || fail "$sftp_tag requires pkg/scp at ${needs:-nothing}, not $version"

	# Consumers that see nothing of this checkout: their own module cache, the
	# public proxy and checksum database, and no workspace or private settings.
	work="$(mktemp -d)"
	trap 'rm -rf "$work"' EXIT
	export GOMODCACHE="$work/modcache" GOPROXY="$proxy" GOSUMDB=sum.golang.org GOFLAGS=-modcacherw GOWORK=off
	export GOPRIVATE="" GONOPROXY="" GONOSUMDB="" GOTOOLCHAIN=local
	consumer "$work/scp" "$version" "$scp_module" scp.NewClient || fail "a program using $scp_tag does not build"
	! grep -q 'github.com/pkg/sftp' "$work/scp/go.sum" || fail "a program using only $scp_tag depends on github.com/pkg/sftp"
	consumer "$work/sftp" "$version" "$sftp_module" sftp.NewClient || fail "a program using $sftp_tag does not build"
	echo "$version is released at ${scp_commit:0:7}: it has its GitHub release, the proxy serves both modules, and programs using each build."
}

case "$step" in
prepare) prepare ;;
tag) tag ;;
verify) verify ;;
*) fail "unknown step $step: prepare, tag or verify" ;;
esac
