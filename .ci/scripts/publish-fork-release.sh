#!/usr/bin/env bash
set -euo pipefail

fail() { echo "$*" >&2; exit 1; }

# Download the fork-release artifact from a successful Fork Release run first.
cd "${1:?usage: publish-fork-release.sh ARTIFACT_DIRECTORY SOURCE_COMMIT}"
expected_source=${2:?expected source commit is required}
repo=mfroembgen/terraform-provider-castai
version=$(jq -er .version provenance.json)
tag=$(jq -er .upstream_tag provenance.json)
upstream_sha=$(jq -er .upstream_commit provenance.json)
source_sha=$(jq -er .source_commit provenance.json)
run_url=$(jq -er .workflow_run provenance.json)
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ && "$tag" == "v$version" ]] || fail "Invalid release version."
[[ "$source_sha" =~ ^[0-9a-f]{40}$ && "$source_sha" == "$expected_source" ]] || fail "Unexpected source commit."
[[ "$upstream_sha" =~ ^[0-9a-f]{40}$ ]] || fail "Invalid upstream commit."
[[ $(jq -er .source_repository provenance.json) == "$repo" ]] || fail "Unexpected source repository."
[[ "$run_url" =~ ^https://github\.com/$repo/actions/runs/[0-9]+$ ]] || fail "Unexpected build URL."
gh api "repos/$repo/actions/runs/${run_url##*/}" | jq -e --arg source "$source_sha" \
  '.head_sha == $source and .status == "completed" and .conclusion == "success"' >/dev/null
shasum -a 256 -c "terraform-provider-castai_${version}_SHA256SUMS"

if gh release view "$tag" --repo "$repo" >/dev/null 2>&1; then
  echo "Release $tag already exists; refusing to replace artifacts." >&2
  exit 1
fi
remote_tag_commit() {
  local refs
  refs=$(git ls-remote --tags "$1" "refs/tags/$tag" "refs/tags/$tag^{}") || return
  printf '%s\n' "$refs" | awk '/\^\{\}$/ { peeled=$1 } !/\^\{\}$/ { direct=$1 } END { print peeled ? peeled : direct }'
}
[[ $(remote_tag_commit https://github.com/castai/terraform-provider-castai.git) == "$upstream_sha" ]] || fail "Upstream tag mismatch or lookup failure."
[[ $(remote_tag_commit "https://github.com/$repo.git") == "$upstream_sha" ]] || fail "Fork tag must already match upstream."

notes=$(mktemp)
trap 'rm -f "$notes"' EXIT
cat > "$notes" <<EOF
Native HPA fork of upstream $tag ($upstream_sha).

Built from [$source_sha](https://github.com/$repo/commit/$source_sha) with $(jq -r .go_version provenance.json).
Preserves native HPA management, ownership, replica bounds, resource metrics and scaling behavior.
Linux amd64 and arm64 archives follow upstream version and binary naming. Checksums include provenance.json.

The Git tag remains on the original upstream commit. Patched source is the source_commit in provenance.json; Go module consumers must use that immutable commit rather than the tag's automatic source archive.

Build: $run_url
EOF
gh release create "$tag" --repo "$repo" --verify-tag --title "$tag" --notes-file "$notes" \
  "terraform-provider-castai_${version}_linux_amd64.zip" \
  "terraform-provider-castai_${version}_linux_arm64.zip" \
  "terraform-provider-castai_${version}_SHA256SUMS" provenance.json
[[ $(remote_tag_commit "https://github.com/$repo.git") == "$upstream_sha" ]] || fail "Published tag verification failed."
