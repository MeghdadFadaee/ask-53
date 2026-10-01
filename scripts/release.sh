#!/usr/bin/env bash
# Build local release artifacts. This script never pushes tags or publishes.
set -euo pipefail

version=${1:-}
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
  echo 'Usage: scripts/release.sh vMAJOR.MINOR.PATCH[-PRERELEASE]' >&2
  exit 2
fi

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
output_dir=${ASK53_RELEASE_DIR:-"$repo_root/dist/$version"}
if [[ -e "$output_dir" ]]; then
  echo "Refusing to overwrite existing release directory: $output_dir" >&2
  exit 2
fi
mkdir -p -- "$output_dir"
# Make paths absolute before entering staging or the output directory.
output_dir=$(cd -- "$output_dir" && pwd)
staging=$(mktemp -d)
trap 'rm -rf -- "$staging"' EXIT
cd -- "$repo_root"

# Exclude local filesystem attributes and normalize ownership on BSD/GNU tar.
case "$(tar --version)" in
  *bsdtar*) tar_flags=(--no-xattrs --no-acls --uid 0 --gid 0 --uname root --gname root) ;;
  *GNU*) tar_flags=(--no-xattrs --no-acls --owner=0 --group=0 --numeric-owner) ;;
  *) echo "Release packaging requires BSD tar or GNU tar" >&2; exit 2 ;;
esac

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  target_os=${target%/*}
  target_arch=${target#*/}
  name="ask53-${version}-${target_os}-${target_arch}"
  package_dir="$staging/$name"
  mkdir -p -- "$package_dir"
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
    go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=$version" \
    -o "$package_dir/ask53" ./cmd/ask53
  cp LICENSE README.md THIRD_PARTY_NOTICES.md config.local.json "$package_dir/"
  cp -R third_party deploy "$package_dir/"
  COPYFILE_DISABLE=1 tar "${tar_flags[@]}" -czf "$output_dir/$name.tar.gz" -C "$staging" "$name"
done

cd -- "$output_dir"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum ./*.tar.gz > SHA256SUMS
else
  shasum -a 256 ./*.tar.gz > SHA256SUMS
fi
printf 'Built release archives and SHA256SUMS in %s\n' "$output_dir"
