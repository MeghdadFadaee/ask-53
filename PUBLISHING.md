# Releasing Ask53

The public source repository is
[MeghdadFadaee/ask-53](https://github.com/MeghdadFadaee/ask-53). The first planned
release is **v0.1.0**. Preparing files or running a local build does not publish it.

## Prepare a release commit

1. Update `CHANGELOG.md` and create `docs/releases/VERSION.md`, including
   installation instructions, changes, and known limitations. v0.1.0 is prepared
   in [its release notes](docs/releases/v0.1.0.md). Keep an `Unreleased` section for
   subsequent work. This project follows 0.x versioning; interfaces may change.
2. Use a supported Go 1.26+ patch release and run:

   ```sh
   go mod verify
   make check
   make test
   make release VERSION=v0.1.0
   make verify-release VERSION=v0.1.0
   bash scripts/test-release.sh v0.1.0
   ```

   Output is `dist/v0.1.0/`: four Linux/macOS amd64/arm64 `.tar.gz` archives and
   `SHA256SUMS`. Verification checks all hashes, exact package contents, and the
   native binary's version/configuration. Output directories cannot be overwritten;
   use `ASK53_RELEASE_DIR=/absolute/new/path` for a fresh candidate build. Generated
   files stay ignored and must not be committed.
3. Review and commit the release preparation, preserving configured commit
   signing. Push the commit to `main` through the normal review process and wait
   for both Linux and macOS test checks to pass. Tag the exact reviewed commit.

Optional: run the **release** workflow manually on that commit's branch/ref with
`version=v0.1.0`. It runs the same checks and uploads archives as a workflow
artifact, without creating a GitHub release. Release notes must exist for that
version. Inspect/download the artifact from the Actions run.

## Create the draft from a tag

After the preparation commit is on `main` and CI is green:

```sh
git switch main
git pull --ff-only
git tag -s v0.1.0 -m 'Ask53 v0.1.0'
git push origin v0.1.0
```

Use your configured signing key; do not disable signing to get around a locked
key. Check the target commit before tagging. If you cannot sign tags, follow your
repository's maintainer policy rather than silently publishing an unsigned tag.

Pushing a `v*` tag starts the **release** workflow. It validates the version/notes,
reuses the full Linux/macOS test workflow at the tagged commit, builds and verifies
all four archives, and uploads a workflow artifact. Only then does a separate job
with `contents: write` create a **draft** GitHub release containing the four
archives and `SHA256SUMS`. Build/test jobs retain read-only repository access.
Prerelease versions such as `v0.2.0-rc.1` are marked as prereleases.

The CLI uses [`--draft` and `--verify-tag`](https://cli.github.com/manual/gh_release_create)
to keep publication manual and require an existing remote tag. No API keys or
repository secrets are required beyond the workflow's scoped `GITHUB_TOKEN`.
Tag creation should use your normal Git credentials; tags pushed by another
workflow using its `GITHUB_TOKEN` generally do not trigger this workflow.

If checks fail, no draft is created. Fix the problem and use a new version if the
tag was already distributed. Re-run failed jobs for transient errors. If draft
creation partially succeeds, inspect its assets before repairing it; the workflow
intentionally does not overwrite an existing release or replace published assets.

## Review and publish

1. Open the draft in GitHub Releases. Confirm its tag/commit, notes, four archives,
   and checksum manifest. Download and extract the archive for your machine:

   ```sh
   # If all four archives were downloaded:
   shasum -a 256 -c SHA256SUMS
   # If only one archive was downloaded:
   # shasum -a 256 --ignore-missing -c SHA256SUMS
   tar -xzf ask53-v0.1.0-darwin-arm64.tar.gz
   ./ask53-v0.1.0-darwin-arm64/ask53 -version
   ```

   On Linux use `sha256sum` and the matching `linux` archive. Missing files are
   deliberately ignored only in the single-archive example; verify the selected
   archive actually reports `OK`. Checksums detect corruption, not independent
   publisher authenticity.
2. Review [TESTING.md](TESTING.md). Linux/macOS automated CI is required. A real
   provider, container runtime, and systemd installation still need validation on
   the intended deployment before calling that deployment production-tested.
   Verify TCP/UDP port 53, firewall/allowlist, and a small provider budget there.
3. Click **Publish release** after this review. Keep existing budget state during
   upgrades; release archives must never contain credentials or quota state.

For manual recovery, download the verified artifact from the tagged run, then:

```sh
gh release create v0.1.0 --draft --verify-tag --title 'Ask53 v0.1.0' \
  --notes-file docs/releases/v0.1.0.md \
  dist/v0.1.0/*.tar.gz dist/v0.1.0/SHA256SUMS
```

Only use files produced at that exact tag/version.

## Repository settings

Maintainers should protect `main`, require review and Linux/macOS checks, restrict
release-tag creation, and enable private vulnerability reporting and secret
scanning/push protection where available. See [SECURITY.md](SECURITY.md). These
host-side settings are managed separately from the source checkout.

The Go module is `github.com/MeghdadFadaee/ask-53`; packages are internal and only
commands are distributed. Prefer release binaries or `make build VERSION=v0.1.0`
from the tagged source to obtain an embedded release version. Plain `go install`
uses the development version unless linker flags are supplied.
