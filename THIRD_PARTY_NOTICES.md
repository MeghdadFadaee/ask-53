# Third-party notices

Ask53's own source is licensed under [MIT](LICENSE). Dependencies retain their
own terms. These notices and the `third_party/` directory accompany binary
release archives and the container image.

| Component | Pinned version | License / notices |
|---|---|---|
| `github.com/miekg/dns` | v1.1.73 | [BSD 3-Clause](third_party/miekg-dns.LICENSE) |
| `golang.org/x/net` | v0.57.0 | [BSD 3-Clause](third_party/x-net.LICENSE), [patent grant](third_party/x-net.PATENTS) |
| `golang.org/x/sys` | v0.47.0 | [BSD 3-Clause](third_party/x-sys.LICENSE), [patent grant](third_party/x-sys.PATENTS) |
| Go runtime and standard library | Selected supported Go build toolchain | [BSD 3-Clause](third_party/go.LICENSE), [patent grant](third_party/go.PATENTS) |

The authoritative dependency versions are in `go.mod` and `go.sum`. Go's
implementation is compiled into the standalone binary. Update these notices
when adding or changing dependencies. The Go version used to build a release
can be inspected using `go version -m` on its binary.

The optional container also includes Alpine Linux and packages installed by its
package manager, including CA certificates. Those components retain their
package-specific licenses and notices; consult the base image and installed
package metadata. They are not relicensed by this project's MIT license.
