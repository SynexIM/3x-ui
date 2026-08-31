#!/usr/bin/env bash
# The panel packages an Xray binary from a SynexIM/xray-core *release tag*, but
# compiles against the xray-core commit go.mod *replaces* to. When those two
# drift the panel still builds and installs; it only fails later over gRPC, on a
# customer node. This gate is the only thing standing between the two.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

die() {
    printf '\033[0;31m[xray-pin] %s\033[0m\n' "$*" >&2
    exit 1
}
note() { printf '[xray-pin] %s\n' "$*"; }

# Every archive .github/workflows/release.yml downloads. Keep in sync with the
# build matrix there and with the Windows job.
REQUIRED_ASSETS=(
    Xray-linux-64.zip
    Xray-linux-32.zip
    Xray-linux-arm64-v8a.zip
    Xray-linux-arm32-v7a.zip
    Xray-linux-arm32-v6.zip
    Xray-linux-arm32-v5.zip
    Xray-linux-s390x.zip
    Xray-windows-64.zip
)

[ -f .xray-version ] || die ".xray-version is missing"
tag="$(tr -d '[:space:]' < .xray-version)"
[ -n "$tag" ] || die ".xray-version is empty"

replace_line="$(grep -E '^replace[[:space:]]+github\.com/xtls/xray-core[[:space:]]+=>' go.mod || true)"
[ -n "$replace_line" ] || die "go.mod has no 'replace github.com/xtls/xray-core =>' line"
module="$(awk '{print $4}' <<< "$replace_line")"
version="$(awk '{print $5}' <<< "$replace_line")"
[ -n "$module" ] && [ -n "$version" ] || die "cannot parse the replace line: $replace_line"

case "$module" in
    github.com/*) slug="${module#github.com/}" ;;
    *) die "the replace target $module is not on github.com; this gate cannot resolve it" ;;
esac

note "go.mod pins $module $version"
note ".xray-version asks for $slug@$tag"

api="https://api.github.com/repos/${slug}"

# Try the ambient token first (higher rate limit), fall back to anonymous: a
# repo-scoped GITHUB_TOKEN from another repository is not always accepted.
gh_get() {
    local url="$1" out
    if [ -n "${GITHUB_TOKEN:-}" ] &&
        out="$(curl -fsSL -H 'Accept: application/vnd.github+json' \
            -H "Authorization: Bearer ${GITHUB_TOKEN}" "$url" 2> /dev/null)"; then
        printf '%s' "$out"
        return 0
    fi
    curl -fsSL -H 'Accept: application/vnd.github+json' "$url"
}

commit_json="$(gh_get "${api}/commits/${tag}" 2> /dev/null)" ||
    die "tag ${tag} does not exist in ${slug} (or the repo is not readable — it must be public)"
tag_sha="$(printf '%s' "$commit_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["sha"])')"
[ -n "$tag_sha" ] || die "could not resolve ${slug}@${tag} to a commit"

if [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-[0-9]{14}-([0-9a-f]{12})$ ]]; then
    pinned_sha12="${BASH_REMATCH[1]}"
    if [ "${tag_sha:0:12}" != "$pinned_sha12" ]; then
        die "MISMATCH: go.mod pins commit ${pinned_sha12}, but ${slug}@${tag} is ${tag_sha:0:12}.
       The panel would be compiled against one xray-core and shipped with another.
       Either point .xray-version at a tag on ${pinned_sha12}, or move the pin to
       the tag. A pseudo-version is v0.0.0-<UTC committer time>-<sha12>:
         go mod edit -replace=github.com/xtls/xray-core=${module}@v0.0.0-<ts>-${tag_sha:0:12}
         go mod tidy"
    fi
    note "OK: ${slug}@${tag} == go.mod pin ${pinned_sha12}"
elif [ "$version" != "$tag" ]; then
    die "MISMATCH: go.mod pins ${version} but .xray-version says ${tag}"
else
    note "OK: go.mod and .xray-version both say ${tag}"
fi

release_json="$(gh_get "${api}/releases/tags/${tag}" 2> /dev/null)" ||
    die "no published GitHub Release for ${slug}@${tag}; a bare git tag carries no binaries"

assets="$(printf '%s' "$release_json" |
    python3 -c 'import json,sys; print("\n".join(a["name"] for a in json.load(sys.stdin)["assets"]))')"

missing=()
for want in "${REQUIRED_ASSETS[@]}"; do
    grep -Fxq "$want" <<< "$assets" || missing+=("$want")
done
if [ ${#missing[@]} -gt 0 ]; then
    die "release ${slug}@${tag} is missing ${#missing[@]} asset(s): ${missing[*]}
       Re-run the xray-core 'Build and Release' workflow for that release; the
       upload step only fires on a release:published event."
fi
note "OK: release ${slug}@${tag} carries all ${#REQUIRED_ASSETS[@]} required archives"
