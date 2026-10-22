#!/usr/bin/env bash
# simh/mkpatches.sh: regenerate simh/patches/*.patch from an edited SimH tree.
#
#   simh/mkpatches.sh [--src DIR]
#
# Workflow: run simh/build.sh (it leaves DIR, default simh/src, at the pinned
# commit with all patches applied), edit the SimH files in DIR, then run this
# script and rebuild.  Every patch keeps its description (the text before the
# first "diff --git" line) and gets the "git diff" against the pinned commit of
# the files it already touches.  To change a file no patch touches yet, add a
# line "diff --git a/<file> b/<file>" to the patch that should carry it first.
# simh/panel/* are copied by build.sh and are not part of any patch.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
src=$here/src
while [ $# -gt 0 ]; do
    case $1 in
    --src) [ $# -ge 2 ] || { echo "mkpatches.sh: --src needs a value" >&2; exit 2; }
           src=$2; shift 2 ;;
    --src=*) src=${1#*=}; shift ;;
    *) sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p}' "$0" >&2; exit 2 ;;
    esac
done
commit=$(tr -d '[:space:]' < "$here/SIMH_COMMIT")
git -C "$src" cat-file -e "$commit^{commit}" ||
    { echo "mkpatches.sh: $src does not have $commit" >&2; exit 1; }

for p in "$here"/patches/*.patch; do
    mapfile -t files < <(sed -n 's|^diff --git a/\([^ ]*\) b/.*|\1|p' "$p" | sort -u)
    [ ${#files[@]} -gt 0 ] || { echo "mkpatches.sh: $p names no files" >&2; exit 1; }
    tmp=$(mktemp "$p.XXXXXX")
    sed '/^diff --git /,$d' "$p" > "$tmp"
    git -C "$src" diff --no-color --no-ext-diff "$commit" -- "${files[@]}" >> "$tmp"
    if git -C "$src" diff --quiet "$commit" -- "${files[@]}"; then
        rm -f "$tmp"
        echo "mkpatches.sh: $(basename "$p"): no changes in ${files[*]}" >&2
        exit 1
    fi
    chmod 0644 "$tmp"
    mv -f "$tmp" "$p"
    echo "mkpatches.sh: wrote $(basename "$p") (${files[*]})"
done
