# First-tag runbook

For the maintainer. Tagging, publishing and every registry login are the maintainer's; agents never do them. The [macOS Docker Desktop run](release-checklist.md) comes first (ROADMAP item 4). `X.Y.Z` below is the version, without the `v`.

## What exists today

- `.goreleaser.yaml` builds linux and darwin binaries for amd64 and arm64, a `tar.gz` archive of each (with LICENSE, NOTICE and README.md), `checksums.txt`, the image `ghcr.io/sustinbebustin/understudy` for linux/amd64 and linux/arm64, and a Homebrew cask. It publishes nothing: the GitHub release is disabled, `dockers_v2` is disabled outside snapshot, and the cask is committed to the tap only when `HOMEBREW_TAP_TOKEN` is set.
- `release.yml` runs `goreleaser release --clean` on a `v*` tag, with `contents: write` and `packages: write` on that job only, and attaches the archives, checksums and cask to the run.
- CI's `release-check` job runs `just release-check` on every pull request: `goreleaser check`, then `goreleaser release --snapshot --clean --skip=publish`. Run it locally the same way; it writes `dist/` (gitignored) and leaves two `ghcr.io/sustinbebustin/understudy:*-amd64`/`-arm64` tag pairs in the local daemon.
- No SBOM and no signing. `dockers_v2.sbom` is off, because the attestation runs BuildKit's scanner image and archive SBOMs need syft, neither of which the release uses today. Turning either on is a separate decision.

## 1. Decide before the tag

- **The cask and Gatekeeper.** The darwin binaries are not signed or notarized. Homebrew quarantines what a cask downloads, so macOS may refuse to run `understudy` installed from the cask. Check it on the first release (step 5). The fixes are signing and notarizing, or a cask `hooks.post.install` that removes the quarantine attribute; the second bypasses Gatekeeper for this binary, so it is a security choice, not a default.
- **The npm and PyPI names.** Both are `testcontainers-understudy` (testcontainers.md 8.1, finding 9); confirm before publishing, or change `package.json`/`pyproject.toml` first.
- **The `latest` image tag.** Every non-snapshot run moves `latest`, a prerelease tag such as `v0.1.0-rc.1` included. The cask already skips prereleases (`skip_upload: auto`).
- **Release notes.** `changelog` is disabled, so the GitHub release body is empty unless you write it after the run or enable the changelog.

## 2. Turn publishing on

One pull request, reviewed and merged before the tag:

1. In `.goreleaser.yaml`, set `release.disable: false`, and remove `disable: "{{ not .IsSnapshot }}"` from `dockers_v2`. Outside snapshot dockers_v2 then builds both platforms and pushes one manifest list per tag in a single step; there is no separate push to hold back.
2. Update the header comments of `.goreleaser.yaml` and `release.yml`, which say nothing is published.
3. Bump the Testcontainers modules to `X.Y.Z` in lockstep (testcontainers.md 8.8):
   - `testcontainers/go/understudy/understudy.go`: `DefaultImage = "ghcr.io/sustinbebustin/understudy:X.Y.Z"`.
   - `testcontainers/go/go.mod`: `(cd testcontainers/go && go mod edit -require=github.com/sustinbebustin/understudy@vX.Y.Z)`. The `replace => ../..` stays, since it applies only inside the repository; consumers resolve the root module at the tag pushed in step 3, which carries the same commit.
   - `testcontainers/python/pyproject.toml` `version` and `src/testcontainers_understudy/__init__.py` `__version__`, then `(cd testcontainers/python && uv lock)`.
   - `testcontainers/node/package.json` `version` and `src/index.ts` `VERSION`.
   - The tests that pin `0.0.0` until the first release: `testcontainers/go/understudy/understudy_test.go`, `testcontainers/python/tests/test_validation.py`, `testcontainers/node/test/validation.test.ts`.
4. Run `just verify`, `just release-check` and `just testcontainers`.

Then add the tap token: a fine-grained personal access token limited to `sustinbebustin/homebrew-tap` with Contents read and write. The workflow's own `GITHUB_TOKEN` cannot write to another repository.

```sh
gh secret set HOMEBREW_TAP_TOKEN --repo sustinbebustin/understudy
```

## 3. Tag

From the merged commit on `main`:

```sh
git switch main && git pull --ff-only
git tag -a vX.Y.Z -m "understudy vX.Y.Z"
git push origin vX.Y.Z
gh run watch "$(gh run list --workflow release.yml --limit 1 --json databaseId --jq '.[0].databaseId')" --exit-status
```

Pushing the tag is the irreversible public step. If the run fails before publishing anything, fix forward with a new patch version rather than moving the tag: the Go module proxy caches a tag the first time anyone asks for it.

## 4. Verify the release

Binaries and checksums:

```sh
gh release view vX.Y.Z --json assets --jq '.assets[].name'
d=$(mktemp -d) && gh release download vX.Y.Z --dir "$d"
(cd "$d" && shasum -a 256 -c checksums.txt)
tar -xzf "$d/understudy_X.Y.Z_$(uname -s | tr A-Z a-z)_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz" -C "$d" && "$d/understudy" version
```

Expect four archives and `checksums.txt`, every line `OK`, and `understudy X.Y.Z`.

The image:

```sh
docker buildx imagetools inspect ghcr.io/sustinbebustin/understudy:X.Y.Z
docker run -d --name understudy-release-check -p 127.0.0.1:4566:4566 ghcr.io/sustinbebustin/understudy:X.Y.Z
curl -fsS http://127.0.0.1:4566/_understudy/v1/health | jq '{ready, version}'
docker rm -f understudy-release-check
```

The index lists `linux/amd64` and `linux/arm64`. Entries for `unknown/unknown` are buildx's provenance attestations, not platforms. Health answers `ready: true` and `version: "X.Y.Z"`. Check the package's visibility at `https://github.com/users/sustinbebustin/packages/container/package/understudy`: make it public if it is not, and link it to the repository if GitHub has not.

## 5. Verify Homebrew

```sh
gh api repos/sustinbebustin/homebrew-tap/contents/Casks/understudy.rb --jq .content | base64 -d | head -20
brew install --cask sustinbebustin/tap/understudy
understudy version
```

The cask's version is `X.Y.Z` and its URLs point at the `vX.Y.Z` release assets. If macOS refuses to run the binary, see "The cask and Gatekeeper" in step 1.

## 6. Publish the Testcontainers modules

In lockstep with the release, and only after the GHCR image exists, since each module's default image is `ghcr.io/sustinbebustin/understudy:X.Y.Z` (testcontainers.md 8.8).

Go, a tag on the same commit:

```sh
git tag -a testcontainers/go/vX.Y.Z -m "testcontainers/go vX.Y.Z" 'vX.Y.Z^{commit}'
git push origin testcontainers/go/vX.Y.Z
```

It does not match `v*`, so it starts no release run. Check that a consumer resolves it, in a scratch module outside the repository:

```sh
(d=$(mktemp -d) && cd "$d" && go mod init example.com/probe \
  && go get github.com/sustinbebustin/understudy/testcontainers/go@vX.Y.Z \
  && go list -m github.com/sustinbebustin/understudy)
```

The last line names `vX.Y.Z`.

Python, with your PyPI credentials or a trusted publisher:

```sh
(cd testcontainers/python && uv build && uv publish)
```

Node, with your npm login:

```sh
(cd testcontainers/node && bun install --frozen-lockfile && bun run build && npm pack --dry-run)
(cd testcontainers/node && npm publish --access public)
```

Read the `npm pack --dry-run` listing before publishing: `package.json` names only `dist/`, and npm adds README.md and package.json itself.

## 7. The registry pull request

After all three are published, open a pull request on `testcontainers/community-module-registry` that adds `modules/understudy/index.md`, with `go`, `python` and `nodejs` entries, `maintainer: official` and category `cloud` (testcontainers.md 8.9). Copy the shape of an existing vendor module's file, such as `modules/microcks/index.md`. Whether `official` is the value for a vendor-published module is inferred from the registry's own entries, not documented.

## 8. After the release

- Install instructions still say to build from a clone: README "Install", `docs/guide/getting-started.md` and `docs/guide/ci.md` gain the binaries, the cask, the GHCR image and the Testcontainers packages.
- ROADMAP item 4 records the tag.
