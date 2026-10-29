#!/bin/sh
# Check the commit message with the same parser that generates the changelog.
set -eu

# The empty range HEAD..HEAD checks only the new message, not the history.
# It needs a HEAD, so the root commit goes unchecked.
git rev-parse -q --verify HEAD >/dev/null || exit 0

msg=$(git stripspace --strip-comments <"$1")

# On a rejected message, git-cliff 2.14.2 panics while it prints the error,
# so its output is replaced with a plain message.
if ! mise exec -- git-cliff HEAD..HEAD --with-commit "$msg" -o /dev/null >/dev/null 2>&1; then
	printf 'Not a Conventional Commit: %s\n' "$(printf '%s\n' "$msg" | head -n 1)" >&2
	printf 'See https://www.conventionalcommits.org/en/v1.0.0/\n' >&2
	exit 1
fi
