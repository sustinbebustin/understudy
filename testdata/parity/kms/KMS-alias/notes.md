# KMS-alias

- Date: 2026-10-14T06:40Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

- status (02-duplicate-raw.http, 01-duplicate.http): matches (AWS HTTP 400; understudy 400 via codegen/overlays/kms.json:24-27 statusOverride; model says 409)
- __type: matches (AlreadyExistsException; internal/service/kms/errors.go:36)
- message: matches (AWS `An alias with the name arn:aws:kms:us-east-2:123456789012:alias/<name> already exists`; understudy same, errors.go:36 with aliasARN keyref.go:180, raised at aliases.go:91; verified against a local run)
- Content-Type: matches (application/x-amz-json-1.1)
- response headers: differs: AWS also sends `Cache-Control: no-cache, no-store, must-revalidate, private`, `Expires: 0`, `Pragma: no-cache` (02-duplicate-raw.http); understudy sends none of them (no such header anywhere in internal/); clients ignore them
- conformance/aws-cli/tests/kms.sh:105 expects 400: matches
- conformance/js-sdk-v3/tests/kms.test.ts:200-204 expects 400 and the message: matches
- overlay evidence label: codegen/overlays/kms.json:28-32 already reads aws-recorded, source testdata/parity/kms/KMS-alias/02-duplicate-raw.http, but that file does not exist yet and docs/coverage.md:1302 still lists aws-doc (needs the capture landed + `just generate`)

