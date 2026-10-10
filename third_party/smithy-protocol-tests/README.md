# Smithy protocol compliance corpus

`model.json` is the Smithy protocol compliance corpus of Smithy 1.74.0 (Apache-2.0) as one flattened JSON AST. `internal/cmd/gen -corpus` reads it to generate the synthetic test services and case tables under `internal/protocoltest/gen`. `PIN.json` records the Smithy version, the CLI archive and the six input jars with their hashes, the exact command, and the size and SHA-256 of `model.json`; `go run ./internal/cmd/vendor -verify` checks them offline.

Do not edit `model.json` by hand. To produce it again, for example when bumping the Smithy version:

1. Download the self-contained Smithy CLI release, which bundles its own runtime, so no system Java is needed: `https://github.com/smithy-lang/smithy/releases/download/1.74.0/smithy-cli-linux-x86_64.zip`. Check its SHA-256 against the `.sha256` file published beside it, unzip it into a fresh temporary directory and `chmod +x bin/smithy`.
2. Download the six jars `PIN.json` lists from Maven Central into the same temporary directory, checking each against the `.sha1` Maven Central publishes beside it.
3. In that directory, run the command `PIN.json` records:

   ```
   smithy ast --no-config --flatten smithy-aws-protocol-tests-1.74.0.jar smithy-protocol-tests-1.74.0.jar smithy-aws-traits-1.74.0.jar smithy-protocol-test-traits-1.74.0.jar smithy-protocol-traits-1.74.0.jar smithy-validation-model-1.74.0.jar > model.json
   ```

   The trait jars must be passed alongside the test jars, or validation fails with `Model.UnresolvedTrait`. `--flatten` removes mixins, which the generator does not resolve. The output is deterministic: two runs give the same bytes.
4. Copy `model.json` here, update `PIN.json` (version, URLs, sizes and hashes), update the version constants in `internal/cmd/vendor/corpus.go` and `internal/codegen/corpus.go`, and run `just generate`.

Background and the measured result are in `docs/research/phase1/smithy-protocol-tests.md` section 5.
