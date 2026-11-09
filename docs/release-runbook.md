# First-tag runbook

For the maintainer. Tagging, publishing and every registry login are the maintainer's; agents never do them. The [macOS Docker Desktop run](release-checklist.md) comes first (ROADMAP item 4). `X.Y.Z` below is the version, without the `v`.

## What exists today

- `.goreleaser.yaml` builds linux and darwin binaries for amd64 and arm64, a `tar.gz` archive of each (with LICENSE, NOTICE and README.md), `checksums.txt`, the image `ghcr.io/sustinbebustin/understudy` for linux/amd64 and linux/arm64, and a Homebrew cask. A tag publishes the GitHub release and the image, and commits the cask to the tap when `HOMEBREW_TAP_TOKEN` is set. A prerelease tag such as `v0.1.0-rc.1` is marked a prerelease, does not move the `latest` image tag and does not touch the tap.
- `release.yml` runs `goreleaser release --clean` on a `v*` tag, with `contents: write` and `packages: write` on that job only, and attaches the archives, checksums and cask to the run. The release notes are the tag's section of `CHANGELOG.md`; a prerelease tag without its own section takes `[Unreleased]`, and any other tag without a section fails before anything is published.
- CI's `release-check` job runs `just release-check` on every pull request: `goreleaser check`, then `goreleaser release --snapshot --clean --skip=publish`. Run it locally the same way; it writes `dist/` (gitignored) and leaves two `ghcr.io/sustinbebustin/understudy:*-amd64`/`-arm64` tag pairs in the local daemon.
- The darwin binaries ship unsigned. The cask removes the quarantine attribute after install, so Gatekeeper does not block them (step 1, "macOS and Gatekeeper"). Signing and notarizing switch on by themselves if the five `MACOS_*` secrets are ever added.
- No SBOM. `dockers_v2.sbom` is off, because the attestation runs BuildKit's scanner image and archive SBOMs need syft, neither of which the release uses today. Turning either on is a separate decision.

## 1. Decide before the tag

- **macOS and Gatekeeper.** Homebrew quarantines what a cask downloads, and Gatekeeper refuses a quarantined binary that Apple has not notarized. The cask's post-install step (`.goreleaser.yaml` `homebrew_casks.hooks`) removes the attribute, as LocalStack's `lstk` cask does; this trusts the binary on the HTTPS download and the cask's sha256 rather than Apple's notarization (maintainer decision 2026-11-06). A darwin archive downloaded by hand from the release is still quarantined: `xattr -d com.apple.quarantine ./understudy` or System Settings, Privacy & Security, "Open Anyway". Nothing needs doing before the tag. To sign and notarize instead, which needs an Apple Developer Program membership, add five repository secrets and the release does it with no other change:
  1. In the Apple Developer portal, create a **Developer ID Application** certificate (Certificates, Identifiers & Profiles; the certificate signing request comes from Keychain Access on a Mac, or `openssl req` anywhere). Export it with its private key as a `.p12` with a password.
  2. In App Store Connect, Users and Access, Integrations, create a **Team** API key with the Developer role. Download the `.p8` (it can be downloaded once) and note its Key ID and the Issuer ID.
  3. Store them:

     ```sh
     base64 -w0 developer-id.p12 | gh secret set MACOS_SIGN_P12 --repo sustinbebustin/understudy
     gh secret set MACOS_SIGN_PASSWORD --repo sustinbebustin/understudy
     base64 -w0 AuthKey_KEYID.p8 | gh secret set MACOS_NOTARY_KEY --repo sustinbebustin/understudy
     gh secret set MACOS_NOTARY_KEY_ID --repo sustinbebustin/understudy
     gh secret set MACOS_NOTARY_ISSUER_ID --repo sustinbebustin/understudy
     ```

     (`base64 -w0` is GNU; on macOS use `base64 -i <file>`.) Delete the local `.p12` and `.p8` afterwards, or keep them in a password manager.
- **The npm and PyPI names.** Both are `testcontainers-understudy` (testcontainers.md 8.1, finding 9), confirmed by the maintainer on 2026-11-05.
- **A release candidate first.** A `vX.Y.Z-rc.N` tag publishes the binaries and the image under the prerelease version and leaves the tap and `latest` alone, so it rehearses every step but the cask (sections 3 and 4). It needs no changelog cut and no module bump.

## 2. Prepare the version

The cask goes to the existing tap `sustinbebustin/homebrew-tap`, which also holds the mws formula under `Formula/`. GoReleaser writes `Casks/understudy.rb` there on the first publish, so the `Casks/` directory need not exist beforehand. Confirm the tap is still reachable, since a missing tap fails the tagged run in the cask step, after the image and the GitHub release are already public:

```sh
gh repo view sustinbebustin/homebrew-tap
```

For a release candidate, skip to section 3. For `X.Y.Z`, one pull request, reviewed and merged before the tag:

1. In `CHANGELOG.md`, move the `[Unreleased]` entries under `## [X.Y.Z] - <date>` and leave `[Unreleased]` empty.
2. Bump the Testcontainers modules to `X.Y.Z` in lockstep (testcontainers.md 8.8):
   - `testcontainers/go/understudy/understudy.go`: `DefaultImage = "ghcr.io/sustinbebustin/understudy:X.Y.Z"`.
   - `testcontainers/go/go.mod`: `(cd testcontainers/go && go mod edit -require=github.com/sustinbebustin/understudy@vX.Y.Z)`. The `replace => ../..` stays, since it applies only inside the repository; consumers resolve the root module at the tag pushed in step 3, which carries the same commit.
   - `testcontainers/python/pyproject.toml` `version` and `src/testcontainers_understudy/__init__.py` `__version__`, then `(cd testcontainers/python && uv lock)`.
   - `testcontainers/node/package.json` `version` and `src/index.ts` `VERSION`.
   - The tests that pin `0.0.0` until the first release: `testcontainers/go/understudy/understudy_test.go`, `testcontainers/python/tests/test_validation.py`, `testcontainers/node/test/validation.test.ts`.
3. Run `just verify`, `just release-check` and `just testcontainers`.

The cask needs the `HOMEBREW_TAP_TOKEN` repository secret: a fine-grained personal access token limited to `sustinbebustin/homebrew-tap` with Contents read and write. The token mws already uses for its `HOMEBREW_TAP_TOKEN` secret qualifies if it has that scope; GitHub never shows a secret's value again, so paste it from where you keep it or create a new one. The workflow's own `GITHUB_TOKEN` cannot write to another repository. Check it exists with `gh secret list --repo sustinbebustin/understudy`; set it with:

```sh
gh secret set HOMEBREW_TAP_TOKEN --repo sustinbebustin/understudy
```

## 3. Tag

From the merged commit on `main`:

```sh
git switch main && git pull --ff-only
git tag -a vX.Y.Z -m "understudy vX.Y.Z"
git push origin vX.Y.Z
sha=$(git rev-parse 'vX.Y.Z^{commit}')
until id=$(gh run list --workflow release.yml --commit "$sha" --limit 1 --json databaseId --jq '.[0].databaseId // empty') && [ -n "$id" ]; do sleep 5; done
gh run watch "$id" --exit-status
```

The loop waits for GitHub to create this tag's run; without it, `gh run list` can return nothing yet, or a previous tag's finished run.

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

A release candidate stops after section 4: it writes no cask and publishes no modules.

```sh
gh api repos/sustinbebustin/homebrew-tap/contents/Casks/understudy.rb --jq .content | base64 -d | head -20
brew install --cask sustinbebustin/tap/understudy
understudy version
```

The cask's version is `X.Y.Z` and its URLs point at the `vX.Y.Z` release assets. On a Mac, `understudy version` must run without a Gatekeeper prompt, and `xattr "$(readlink -f "$(command -v understudy)")"` must not list `com.apple.quarantine`. If it does, the cask's post-install step did not run: read the cask in the tap and `brew install --cask --verbose` output.

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
