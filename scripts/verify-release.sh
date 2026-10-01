#!/usr/bin/env bash
# Verify locally built release packages, including the native binary when present.
set -euo pipefail

version=${1:-}
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
  echo 'Usage: scripts/verify-release.sh vMAJOR.MINOR.PATCH[-PRERELEASE] [DIRECTORY]' >&2
  exit 2
fi
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
output_dir=${2:-${ASK53_RELEASE_DIR:-"$repo_root/dist/$version"}}
output_dir=$(cd -- "$output_dir" && pwd)
staging=$(mktemp -d)
trap 'rm -rf -- "$staging"' EXIT

cd -- "$output_dir"
test "$(wc -l < SHA256SUMS | tr -d ' ')" = 4
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  archive="ask53-$version-${target%/*}-${target#*/}.tar.gz"
  test -s "$archive"
  # Every expected archive must occur exactly once in the manifest.
  test "$(awk -v file="./$archive" '$2 == file {count++} END {print count+0}' SHA256SUMS)" = 1
done
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum -c SHA256SUMS
else
  shasum -a 256 -c SHA256SUMS
fi

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) native=linux/amd64 ;;
  Linux/aarch64|Linux/arm64) native=linux/arm64 ;;
  Darwin/x86_64) native=darwin/amd64 ;;
  Darwin/arm64) native=darwin/arm64 ;;
  *) native= ;;
esac

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  name="ask53-$version-${target%/*}-${target#*/}"
  # Exact membership excludes credentials, quota state, IDE files and tar metadata.
  cat > "$staging/members" <<'EOF'

ask53
LICENSE
README.md
CHANGELOG.md
TESTING.md
CONTRIBUTING.md
SECURITY.md
PUBLISHING.md
THIRD_PARTY_NOTICES.md
config.local.json
deploy/
deploy/ask53.service
deploy/config.example.json
third_party/
third_party/go.LICENSE
third_party/go.PATENTS
third_party/miekg-dns.LICENSE
third_party/x-net.LICENSE
third_party/x-net.PATENTS
third_party/x-sys.LICENSE
third_party/x-sys.PATENTS
EOF
  if [[ -f "$repo_root/docs/releases/$version.md" ]]; then
    printf 'docs/\ndocs/releases/\ndocs/releases/%s.md\n' "$version" >> "$staging/members"
  fi
  LC_ALL=C sed "s|^|$name/|" "$staging/members" | LC_ALL=C sort > "$staging/expected"
  tar -tzf "$name.tar.gz" | LC_ALL=C sort > "$staging/actual"
  diff -u "$staging/expected" "$staging/actual"
  # Release packages consist solely of regular files and directories.
  tar -tvzf "$name.tar.gz" | awk 'substr($0,1,1) != "-" && substr($0,1,1) != "d" {bad=1} END {exit bad}'
  tar -xzf "$name.tar.gz" -C "$staging"
  test -x "$staging/$name/ask53"
  if [[ "$target" == "$native" ]]; then
    test "$("$staging/$name/ask53" -version)" = "$version"
    "$staging/$name/ask53" -config "$staging/$name/config.local.json" -check-config
  fi
done
printf 'Verified four release archives for %s\n' "$version"
