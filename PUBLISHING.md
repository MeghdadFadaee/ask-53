# Publishing Ask53

This checkout is prepared for a public source repository. It does not contain a
remote URL, GitHub repository, published release, or registry credentials.

## Publish the source repository

1. Review the source and [MIT license](LICENSE). Actual credentials and private
   client networks must stay in ignored local files or outside this checkout.
2. Create an empty public repository on your preferred host. On GitHub, avoid
   precreating a README/license because this checkout already provides them.
3. If this checkout has no commits yet, create the initial commit first:

   ```sh
   git diff --cached --check
   git commit -m 'Initial public-ready Ask53 implementation'
   ```

   If your Git configuration requires a signature, make the configured signing
   key available locally before committing. The preparation process does not
   disable signing or handle private-key passphrases.

   Add the repository's actual URL and push the initial branch:

   ```sh
   git remote add origin YOUR_REPOSITORY_URL
   git push -u origin main
   ```

4. On GitHub, wait for both Linux and macOS test jobs to pass. Protect `main`,
   require review and the test checks, enable private vulnerability reporting,
   and enable secret scanning/push protection where available. The configuration
   files here cannot enable host-side settings before a remote exists.
5. Add a repository description, for example: “Ask concise AI questions through
   DNS TXT records, with bounded spending, caching, and TCP safeguards.” Suggested
   topics: `dns`, `go`, `ai`, `txt-records`, `openai-compatible`.

The current Go module is intentionally `ask53`, with only internal packages and
commands. Users can clone and build it using `make build`. If you want remote
`go install` support, first choose the final host/owner/repository URL, change
`module ask53` and the internal import prefixes to that canonical path, and
rerun the tests. Do not advertise an unconfigured install URL.

## Prepare a binary release

Use a supported Go patch release. Choose the release version, review the
changelog, and run:

```sh
make check
make test
make release VERSION=v0.1.0
```

The output directory is `dist/v0.1.0/`, containing four `.tar.gz` archives and
`SHA256SUMS`. Each archive contains `ask53`, the MIT license, dependency notices,
README, and example configurations/deployment files. Archives contain no API
keys, runtime budgets, IDE files, or private configuration. The local mock
provider remains a source-checkout development tool, not a release daemon.

Extract and verify the binary appropriate for your current OS/architecture:

```sh
cd dist/v0.1.0
# Linux:
sha256sum -c SHA256SUMS
# macOS alternative:
# shasum -a 256 -c SHA256SUMS
tar -xzf ask53-v0.1.0-linux-amd64.tar.gz
./ask53-v0.1.0-linux-amd64/ask53 -version
```

Select the `darwin` archive when testing on macOS. Retain checksum and license
files when redistributing. Do not package your working-directory secrets into a
handmade archive of the entire checkout.

The **build release archives** GitHub Actions workflow can be started manually
with a version string. It uploads a downloadable workflow artifact under
read-only repository permissions; it does not create tags, GitHub releases, or
container registry pushes. The regular test workflow also checks packaging on
Linux with an unpublished CI version.

Once the exact release commit's CI is green and its archives are verified:

```sh
git tag -a v0.1.0 -m 'Ask53 v0.1.0'
git push origin v0.1.0
```

Create a release on the repository host from that tag and attach only the four
archives and `SHA256SUMS`. Use the build artifacts from that same commit and
version, not an older local build. Publish the tested limitations from
[TESTING.md](TESTING.md); do not claim an untested live provider or deployment.

Before an actual production release, run the systemd/container installation on
a target Linux host, verify both TCP and UDP port 53 with the intended firewall
and allowlist, and exercise the selected real provider with a deliberately small
budget. Keep existing quota state through upgrades.
