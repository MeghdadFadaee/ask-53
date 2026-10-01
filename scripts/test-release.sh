#!/usr/bin/env bash
# Exercise release validation against real build artifacts; never publish.
set -euo pipefail
version=${1:-}
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
output_dir=${2:-${ASK53_RELEASE_DIR:-"$repo_root/dist/$version"}}
output_dir=$(cd -- "$output_dir" && pwd)
staging=$(mktemp -d)
trap 'rm -rf -- "$staging"' EXIT

reject() {
  if "$@" > "$staging/rejection.log" 2>&1; then
    echo "Expected release validation to reject: $*" >&2
    exit 1
  fi
}
reset_candidate() {
  rm -rf -- "$staging/candidate" "$staging/extracted"
  mkdir "$staging/candidate"
  cp "$output_dir/"*.tar.gz "$output_dir/SHA256SUMS" "$staging/candidate/"
}
rehash() {
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$staging/candidate" && sha256sum ./*.tar.gz > SHA256SUMS)
  else
    (cd "$staging/candidate" && shasum -a 256 ./*.tar.gz > SHA256SUMS)
  fi
}

bash "$repo_root/scripts/verify-release.sh" "$version" "$output_dir"
for invalid in '' v1.2 '../invalid' 'v1.2.3;echo invalid'; do
  reject bash "$repo_root/scripts/verify-release.sh" "$invalid" "$output_dir"
  reject env ASK53_RELEASE_DIR="$staging/invalid" bash "$repo_root/scripts/release.sh" "$invalid"
done
reject env ASK53_RELEASE_DIR="$output_dir" bash "$repo_root/scripts/release.sh" "$version"

archive="ask53-$version-linux-amd64.tar.gz"
reset_candidate
rm "$staging/candidate/$archive"
reject bash "$repo_root/scripts/verify-release.sh" "$version" "$staging/candidate"

reset_candidate
printf 'corruption' >> "$staging/candidate/$archive"
reject bash "$repo_root/scripts/verify-release.sh" "$version" "$staging/candidate"

reset_candidate
head -n 3 "$output_dir/SHA256SUMS" > "$staging/candidate/SHA256SUMS"
reject bash "$repo_root/scripts/verify-release.sh" "$version" "$staging/candidate"

# A checksum-correct archive must still reject extra private files.
reset_candidate
mkdir "$staging/extracted"
tar -xzf "$staging/candidate/$archive" -C "$staging/extracted"
printf 'fake-secret\n' > "$staging/extracted/${archive%.tar.gz}/api-key"
COPYFILE_DISABLE=1 tar -czf "$staging/candidate/$archive" -C "$staging/extracted" "${archive%.tar.gz}"
rehash
reject bash "$repo_root/scripts/verify-release.sh" "$version" "$staging/candidate"

# Even a permitted member name cannot be a symlink.
reset_candidate
mkdir "$staging/extracted"
tar -xzf "$staging/candidate/$archive" -C "$staging/extracted"
rm "$staging/extracted/${archive%.tar.gz}/config.local.json"
ln -s /dev/null "$staging/extracted/${archive%.tar.gz}/config.local.json"
COPYFILE_DISABLE=1 tar -czf "$staging/candidate/$archive" -C "$staging/extracted" "${archive%.tar.gz}"
rehash
reject bash "$repo_root/scripts/verify-release.sh" "$version" "$staging/candidate"

# A checksum-correct native archive must report the requested embedded version.
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) native=linux-amd64 ;;
  Linux/aarch64|Linux/arm64) native=linux-arm64 ;;
  Darwin/x86_64) native=darwin-amd64 ;;
  Darwin/arm64) native=darwin-arm64 ;;
  *) echo 'Native version test requires Linux/macOS amd64/arm64' >&2; exit 1 ;;
esac
reset_candidate
archive="ask53-$version-$native.tar.gz"
mkdir "$staging/extracted"
tar -xzf "$staging/candidate/$archive" -C "$staging/extracted"
printf '#!/bin/sh\nprintf "wrong-version\\n"\n' > "$staging/extracted/${archive%.tar.gz}/ask53"
COPYFILE_DISABLE=1 tar -czf "$staging/candidate/$archive" -C "$staging/extracted" "${archive%.tar.gz}"
rehash
reject bash "$repo_root/scripts/verify-release.sh" "$version" "$staging/candidate"

printf 'Release validation failure cases passed for %s\n' "$version"
