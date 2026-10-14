#!/usr/bin/env bash
# Writes README.md's Changelog section from CHANGELOG.md: the last ten released
# versions, each with its entries, and a link to the rest. The section runs from
# its "## Changelog" heading to the next "## " heading. With --check it writes
# nothing, and fails when the section is not what it would write.
#
# This is the shape github-guard's git-changelog guard expects: versions as
# "### vX.Y.Z" headings, at most ten, and a link to CHANGELOG.md.
#
# Usage: scripts/changelog-readme.sh [--check]
set -euo pipefail
cd "$(dirname "$0")/.."

check=false
case "${1:-}" in
"") ;;
--check) check=true ;;
*)
	echo "usage: scripts/changelog-readme.sh [--check]" >&2
	exit 2
	;;
esac

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The section: every version heading, "## vX.Y.Z — date", becomes a "###" one;
# anything before the first version, such as Unreleased, is left out.
{
	printf '## Changelog\n\n'
	printf 'The last ten releases. Every release is in [CHANGELOG.md](CHANGELOG.md), and on the\n'
	printf '[releases page](https://github.com/antimatter-studios/go-scp/releases).\n\n'
	awk '
		/^## v[0-9]/ { if (++n > 10) exit; sub(/^## /, "### "); print ""; print; next }
		/^## / { if (n) exit; next }
		n { print }
	' CHANGELOG.md
} | cat -s | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}' >"$work/section"

if ! grep -q '^## Changelog$' README.md; then
	echo "changelog-readme: README.md has no \"## Changelog\" section to write" >&2
	exit 1
fi
awk -v section="$work/section" '
	$0 == "## Changelog" {
		while ((getline line < section) > 0) print line
		replacing = 1
		next
	}
	replacing && /^## / { replacing = 0; print "" }
	!replacing { print }
' README.md >"$work/README.md"

if $check; then
	if ! cmp -s README.md "$work/README.md"; then
		echo "changelog-readme: README.md's Changelog section is not CHANGELOG.md's last ten releases; run scripts/changelog-readme.sh" >&2
		diff -u README.md "$work/README.md" >&2 || true
		exit 1
	fi
	exit 0
fi
cp "$work/README.md" README.md
