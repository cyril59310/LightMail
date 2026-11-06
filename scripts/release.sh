#!/usr/bin/env bash
# Publishes a LightMail release on GitHub.
#
#   scripts/release.sh            release the version of frontend/package.json
#   scripts/release.sh patch      bump 0.2.1 -> 0.2.2, then release
#   scripts/release.sh minor      bump 0.2.1 -> 0.3.0
#   scripts/release.sh major      bump 0.2.1 -> 1.0.0
#   scripts/release.sh 1.4.0      set an explicit version (1.4.0-rc.1 = pre-release)
#   add -y to skip the confirmation
#
# The script checks the working tree, commits the version bump, creates the
# vX.Y.Z tag and pushes it. The GitHub Actions workflow
# (.github/workflows/release.yml) then runs the tests, builds the binaries and
# the Docker image, and publishes the release with its notes.
set -euo pipefail

cd "$(dirname "$0")/.."

bump=""
yes=false
for arg in "$@"; do
  case "$arg" in
    -y | --yes) yes=true ;;
    -h | --help) sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    patch | minor | major) bump="$arg" ;;
    [0-9]*.[0-9]*.[0-9]*) bump="$arg" ;;
    *) echo "unknown argument: $arg (see --help)" >&2; exit 2 ;;
  esac
done

die() { echo "✗ $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
need git
need node
need npm

branch="$(git rev-parse --abbrev-ref HEAD)"
[ "$branch" = "master" ] || [ "$branch" = "main" ] || die "releases are made from master (current branch: $branch)"
[ -z "$(git status --porcelain)" ] || die "the working tree has uncommitted changes: commit or stash them first"

echo "→ fetching origin…"
git fetch --quiet --tags origin
if git rev-parse --verify --quiet "origin/$branch" >/dev/null; then
  [ "$(git rev-list --count "HEAD..origin/$branch")" = "0" ] || die "origin/$branch has commits you do not have: pull first"
fi

current="$(node -p "require('./frontend/package.json').version")"
version="$current"
if [ -n "$bump" ]; then
  version="$(node -e '
    const [cur, b] = process.argv.slice(1)
    const m = /^(\d+)\.(\d+)\.(\d+)/.exec(cur)
    if (!m) process.exit(1)
    let [ma, mi, pa] = m.slice(1).map(Number)
    if (b === "major") console.log(`${ma + 1}.0.0`)
    else if (b === "minor") console.log(`${ma}.${mi + 1}.0`)
    else if (b === "patch") console.log(`${ma}.${mi}.${pa + 1}`)
    else console.log(b)
  ' "$current" "$bump")" || die "cannot compute the next version from $current"
fi
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "invalid version: $version"
tag="v$version"

git rev-parse --verify --quiet "refs/tags/$tag" >/dev/null && die "tag $tag already exists (bump the version: patch, minor or major)"
git ls-remote --exit-code --tags origin "refs/tags/$tag" >/dev/null 2>&1 && die "tag $tag already exists on GitHub"

last="$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || true)"
echo
echo "  version   : $current → $version"
echo "  tag       : $tag (branch $branch)"
if [ -n "$last" ]; then
  echo "  commits since $last :"
  git log --oneline "$last..HEAD" | sed 's/^/    /' | head -30
else
  echo "  first release"
fi
echo

if ! $yes; then
  read -r -p "Publish LightMail $version on GitHub? [y/N] " answer
  case "$answer" in y | Y | o | O | yes | oui) ;; *) echo "cancelled"; exit 1 ;; esac
fi

if [ "$version" != "$current" ]; then
  echo "→ bumping frontend/package.json to $version"
  (cd frontend && npm version "$version" --no-git-tag-version --allow-same-version >/dev/null)
  git add frontend/package.json frontend/package-lock.json
  git commit --quiet -m "Version $version"
fi

git tag -a "$tag" -m "LightMail $version"
echo "→ pushing $branch and $tag"
git push --quiet origin "$branch"
git push --quiet origin "$tag"

repo="$(git remote get-url origin | sed -E 's#(git@github.com:|https://github.com/)##; s#\.git$##')"
echo
echo "✓ $tag pushed. GitHub Actions is now building and publishing the release:"
echo "    https://github.com/$repo/actions/workflows/release.yml"
echo "    https://github.com/$repo/releases/tag/$tag (once the workflow is done)"
