#!/bin/bash

# Rebases a git branch off of another git branch, fixing gx-go path conflicts
# along the way. Note: This *can't* fix conflicts in package.json. You'll have to fix those yourself.

set -e

if ! [[ -e .git/rebase-apply/rebasing ]]; then
    current="$(git rev-parse --abbrev-ref HEAD)"
    base="${1:-master}"
    branch_pt="$(git rev-list --reverse ${base}.. | head -1)"
    new="${2:-${current}-fixed}"

    echo ">> Creating a glue commit..."

    # Add a "glue" commit without the gx paths.
    git checkout -B "gx-rebase-from-${new}" "$base"
    gx install
    gx-go rw --fix
    git add -u
    git commit -m "GLUE COMMIT"
    git tag "gx-rebase-glue-${new}"

    echo ">> Un-Rewriting..."

    # Remove the gx paths from *our* branch
    git checkout -B "${new}" "${current}"
    git filter-branch -f --tag-name-filter cat --tree-filter 'gx install && gx-go rw --fix' "${branch_pt}^.."

    echo ">> Rebasing..."

    # Rebase onto this glue commit.
    if ! git rebase "gx-rebase-from-${new}"; then
        echo "gx-rebase failed. Please continue by running gx-rebase after fixing the conflicts."
        exit 1
    fi
else
    head_ref="$(<.git/rebase-apply/head-name)"
    new="${head_ref##refs/heads/}"
    if ! git branch "${new}" --contains "gx-rebase-glue-${new}" >/dev/null 2>&1; then
        echo "unexpected rebase in progress" >&2
        exit 1;
    fi
    while read -p "gx-rebase in progress. Continue? " CONT; do
        case "$CONT" in
            Y|y) break ;;
            N|n) exit 0 ;;
            *) continue ;;
        esac
    done
    git rebase --continue
fi
git branch -D "gx-rebase-from-${new}"

echo ">> Rewriting..."

# Re-add the gx paths
git filter-branch -f --tree-filter 'gx-go rw' "gx-rebase-glue-${new}^.."

echo ">> Removing the glue commit..."

# Remove the glue commit
git rebase "gx-rebase-glue-${new}" --onto "gx-rebase-glue-${new}^"
git tag -d "gx-rebase-glue-${new}"

echo ">> DONE!"

echo ""
echo "WARNING: This command may have totally borked your git history."
echo "WARNING: Please check it with \`git log -p \"${new}..\"'."
