# Release runbook

How to cut an understudy release. A maintainer tags, publishes and holds every registry login; automation and coding agents never do. `X.Y.Z` below is the version, without the `v`.

## What a release publishes

Pushing a `v*` tag runs `release.yml`, which runs GoReleaser (`.goreleaser.yaml`) and then publishes the Testcontainers modules:

| Artifact | Where | Release `vX.Y.Z` | Release candidate `vX.Y.Z-rc.N` |
|---|---|---|---|
| Binaries for linux and darwin on amd64 and arm64, each a `tar.gz` with LICENSE, NOTICE and README.md, and `checksums.txt` | the GitHub release | yes | yes, marked a prerelease |
| Image for linux/amd64 and linux/arm64 | `ghcr.io/sustinbebustin/understudy:X.Y.Z` | yes, and `latest` moves | yes, and `latest` stays |
| Homebrew cask | `Casks/understudy.rb` in `sustinbebustin/homebrew-tap` | yes | no |
| Testcontainers modules | PyPI and npm (`testcontainers-understudy`), the Go tag `testcontainers/go/vX.Y.Z` | yes | no |

- The release notes are the tag's section of `CHANGELOG.md`. A prerelease tag without a section of its own takes `[Unreleased]`; any other tag without a section fails before anything is published.
- The darwin binaries ship unsigned, and the cask removes the quarantine attribute after install ([macOS and Gatekeeper](#macos-and-gatekeeper)).
- No SBOM is published. `dockers_v2.sbom` is off because the attestation runs BuildKit's scanner image and archive SBOMs need syft, neither of which the release uses.
- The archives, the checksums and the cask are also attached to the workflow run.

CI's `release-check` job runs `just release-check` on every pull request: `goreleaser check`, then `goreleaser release --snapshot --clean --skip=publish`. Run it locally the same way after changing `.goreleaser.yaml`, the `Dockerfile` or `release.yml`. It needs Docker with buildx, writes `dist/` (gitignored), and leaves one `ghcr.io/sustinbebustin/understudy:*-amd64` and one `*-arm64` image per tag in the local daemon. `release.yml` and the justfile's `goreleaser_version` pin the same GoReleaser; bump both together.

## 1. Before the tag

- **Run the pre-release manual run.** CI has only Linux, so [the macOS Docker Desktop run](release-checklist.md) is done by hand, on the commit to be released. It matters most when the release changes Lambda, DynamoDB Local, container networking or the Testcontainers modules.
- **Decide whether to rehearse with a release candidate.** A `vX.Y.Z-rc.N` tag publishes the binaries and the image under the prerelease version, leaves `latest` and the tap alone, and publishes no modules. It rehearses every step but the cask and the modules. It needs no changelog cut and no version bump: skip section 2, tag it as in section 3, and stop after section 4.
- **Check the tap and its token.** The cask step runs after the GitHub release and the image are public, so a missing tap or token leaves a release without its cask. Both commands must succeed, and the second must list `HOMEBREW_TAP_TOKEN` ([appendix](#appendix-one-time-setup)):

  ```sh
  gh repo view sustinbebustin/homebrew-tap
  gh secret list --repo sustinbebustin/understudy
  ```

## 2. Prepare the version

Write the release's entries under `[Unreleased]` in `CHANGELOG.md` and merge them. Then run the `prepare-release` workflow:

```sh
gh workflow run prepare-release.yml --repo sustinbebustin/understudy -f version=X.Y.Z
```

It runs `scripts/bump-version X.Y.Z` (`just bump X.Y.Z` locally) and opens the pull request `build: release X.Y.Z` from the branch `release/vX.Y.Z`. The script:

- moves the `[Unreleased]` entries under `## [X.Y.Z] - <date>`, and refuses when `[Unreleased]` is empty;
- moves every version pin to `X.Y.Z`: the Testcontainers modules' versions and default image, the Go module's requirement on the root module, and the example workflow's `UNDERSTUDY_VERSION`. The pins are the script's `PINS`;
- refuses a version that is not `X.Y.Z` or is not newer than the current one, and relocks `testcontainers/python/uv.lock`.

The modules are released in lockstep with understudy because each module's default image is the release's (`docs/research/testcontainers.md` 8.8). Go's `replace => ../..` stays, since it applies only inside the repository: consumers resolve the root module at the tag pushed in section 3, which carries the same commit.

A pull request opened by a workflow starts no `pull_request` run, so the workflow dispatches `ci.yml` on the branch and its jobs report on the same commit. Merge the pull request once they pass. CI's `just sealed-test` fails when the pins disagree (`scripts/bump-version --check` names the file), and each publishing job of `release.yml` first checks that its module's version is the tag's.

## 3. Tag

From the merge commit on `main`:

```sh
git switch main && git pull --ff-only
git tag -a vX.Y.Z -m "understudy vX.Y.Z"
git push origin vX.Y.Z
sha=$(git rev-parse 'vX.Y.Z^{commit}')
until id=$(gh run list --workflow release.yml --commit "$sha" --limit 1 --json databaseId --jq '.[0].databaseId // empty') && [ -n "$id" ]; do sleep 5; done
gh run watch "$id" --exit-status
```

The loop waits for GitHub to create this tag's run. Without it, `gh run list` can return nothing yet, or a previous tag's finished run.

Pushing the tag is the irreversible public step: the Go module proxy caches a tag the first time anyone asks for it. If the run fails, never move the tag. Fix forward with a new patch version, and say in its changelog section what the failed version lacks, as 0.1.1 and 0.1.2 do for the cask.

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

The index lists `linux/amd64` and `linux/arm64`. Entries for `unknown/unknown` are buildx's provenance attestations, not platforms. Health answers `ready: true` and `version: "X.Y.Z"`.

A release candidate stops here.

## 5. Verify Homebrew

```sh
gh api repos/sustinbebustin/homebrew-tap/contents/Casks/understudy.rb --jq .content | base64 -d | head -20
brew install --cask sustinbebustin/tap/understudy
understudy version
```

The cask's version is `X.Y.Z` and its URLs point at the `vX.Y.Z` release assets. On a Mac, `understudy version` must run without a Gatekeeper prompt, and `xattr "$(readlink -f "$(command -v understudy)")"` must not list `com.apple.quarantine`. If it does, the cask's post-install step did not run: read the cask in the tap and the output of `brew install --cask --verbose`.

## 6. Verify the Testcontainers modules

`release.yml` publishes the modules after the release job, so only once the image exists. The `pypi` and `npm` jobs publish through trusted publishing (OIDC, no token), each skipping a version the registry already has, so a rerun succeeds. `go-module` pushes the tag `testcontainers/go/vX.Y.Z` on the same commit and checks that a consumer resolves it.

Check all three:

```sh
gh run view "$id" --json jobs --jq '.jobs[] | "\(.name) \(.conclusion)"'
npm view testcontainers-understudy@X.Y.Z version
curl -fsS https://pypi.org/pypi/testcontainers-understudy/X.Y.Z/json | jq -r .info.version
git ls-remote --tags origin testcontainers/go/vX.Y.Z
```

### Redoing a module by hand

Prefer rerunning the failed job. Outside the workflow, Go is a tag on the release's commit. It does not match `v*`, so it starts no release run:

```sh
git tag -a testcontainers/go/vX.Y.Z -m "testcontainers/go vX.Y.Z" 'vX.Y.Z^{commit}'
git push origin testcontainers/go/vX.Y.Z
```

Check that a consumer resolves it, in a scratch module outside the repository. The last line names `vX.Y.Z`:

```sh
(d=$(mktemp -d) && cd "$d" && go mod init example.com/probe \
  && go get github.com/sustinbebustin/understudy/testcontainers/go@vX.Y.Z \
  && go list -m github.com/sustinbebustin/understudy)
```

Python, with your PyPI credentials:

```sh
(cd testcontainers/python && uv build && uv publish)
```

Node, with your npm login. Read the `npm pack --dry-run` listing before publishing: `package.json` names only `dist/`, and npm adds README.md and package.json itself.

```sh
(cd testcontainers/node && bun install --frozen-lockfile && bun run build && npm pack --dry-run)
(cd testcontainers/node && npm publish --access public)
```

## 7. After the release

The install docs name no version except the example workflow's `UNDERSTUDY_VERSION`, which `scripts/bump-version` moves with the modules. A release needs no documentation change beyond its changelog section.

## Appendix: one-time setup

All of this is in place. It is recorded for the day a token expires, a registry setting is lost or the repository moves.

### The tap token

The cask needs the `HOMEBREW_TAP_TOKEN` repository secret: a fine-grained personal access token limited to `sustinbebustin/homebrew-tap` with Contents read and write. The workflow's own `GITHUB_TOKEN` cannot write to another repository. GitHub never shows a secret's value again, so paste it from where you keep it or create a new one:

```sh
gh secret set HOMEBREW_TAP_TOKEN --repo sustinbebustin/understudy
```

Without the secret the release still succeeds and only skips the tap. `.goreleaser.yaml` must name the token as the single environment variable `{{ .Env.HOMEBREW_TAP_TOKEN }}`, with no template function around it; `internal/distribution/goreleaser_test.go` holds that.

### The image's visibility

A new GHCR package starts private. At `https://github.com/users/sustinbebustin/packages/container/package/understudy`, make it public and link it to the repository.

### Trusted publishing

Both registries trust the workflow `release.yml`, with no environment, in the repository `sustinbebustin/understudy`:

- PyPI: a trusted publisher on the project `testcontainers-understudy` (pypi.org, the project's Publishing settings). For a project that does not exist yet, a pending publisher in the account's Publishing settings creates it on the first publish.
- npm: a trusted publisher in the package's settings. npm configures one only for a package that exists, so a new package's first version is published by hand ([Redoing a module by hand](#redoing-a-module-by-hand)).

### The Testcontainers community registry

The modules are listed by a pull request on `testcontainers/community-module-registry` that adds `modules/understudy/index.md`, with `go`, `python` and `nodejs` entries, `maintainer: official` and category `cloud` (`docs/research/testcontainers.md` 8.9). Copy the shape of an existing vendor module's file, such as `modules/microcks/index.md`. That `official` is the value for a vendor-published module is inferred from the registry's own entries, not documented.

### macOS and Gatekeeper

Homebrew quarantines what a cask downloads, and Gatekeeper refuses a quarantined binary that Apple has not notarized. The cask's post-install step (`.goreleaser.yaml`, `homebrew_casks.hooks`) removes the attribute, as LocalStack's `lstk` cask does. This trusts the binary on the HTTPS download and the cask's sha256 rather than on Apple's notarization. A darwin archive downloaded by hand from the release is still quarantined: `xattr -d com.apple.quarantine ./understudy`, or System Settings, Privacy & Security, "Open Anyway".

To sign and notarize instead, which needs an Apple Developer Program membership, add five repository secrets. The release then does both with no other change:

1. In the Apple Developer portal, create a **Developer ID Application** certificate (Certificates, Identifiers & Profiles; the certificate signing request comes from Keychain Access on a Mac, or `openssl req` anywhere). Export it with its private key as a `.p12` with a password.
2. In App Store Connect, Users and Access, Integrations, create a **Team** API key with the Developer role. Download the `.p8` (it can be downloaded once) and note its Key ID and the Issuer ID.
3. Store them. `base64 -w0` is GNU; on macOS use `base64 -i <file>`:

   ```sh
   base64 -w0 developer-id.p12 | gh secret set MACOS_SIGN_P12 --repo sustinbebustin/understudy
   gh secret set MACOS_SIGN_PASSWORD --repo sustinbebustin/understudy
   base64 -w0 AuthKey_KEYID.p8 | gh secret set MACOS_NOTARY_KEY --repo sustinbebustin/understudy
   gh secret set MACOS_NOTARY_KEY_ID --repo sustinbebustin/understudy
   gh secret set MACOS_NOTARY_ISSUER_ID --repo sustinbebustin/understudy
   ```

4. Delete the local `.p12` and `.p8`, or keep them in a password manager.
