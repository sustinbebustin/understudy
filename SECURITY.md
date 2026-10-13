# Security

understudy is a development and test tool. It emulates AWS's APIs; it is not a security boundary and does not enforce AWS's access controls. This page says what it does protect, what it deliberately does not, and how to report a vulnerability.

## Reporting a vulnerability

Report it privately through GitHub's private vulnerability reporting on this repository (the Security tab, "Report a vulnerability"). Do not open a public issue.

Include the understudy version or commit (`understudy version`), how understudy was started, the steps or a request that shows the problem, and what an attacker gains. The maintainer will acknowledge the report, work with you on a fix, and agree on a disclosure date. understudy is pre-release, so only the latest commit on `main` receives fixes.

## What understudy protects

These are promises; a way around any of them is a vulnerability:

- **Loopback by default.** `serve` binds `127.0.0.1`. Listening on any other address on a host needs an admin token (`UNDERSTUDY_ADMIN_TOKEN`, at least 16 characters) for the admin API under `/_understudy/` and an explicit `--allow-unauthenticated-aws-api`. Inside a container with its own network, the published port is the control instead, and the image documents publishing it on `127.0.0.1` only.
- **Browsers and DNS rebinding.** A loopback listener answers only host names an outside DNS server cannot point at it. The AWS API answers a browser only from a page on a loopback origin with a port other than understudy's own, so a page from another site cannot drive it.
- **Files.** S3 keys never become file paths: object bodies are stored by content hash under a rooted directory. Lambda zip extraction refuses non-local names and symlinks and caps sizes. Snapshot loading checks every archive entry name before writing.
- **Input limits.** Request bodies and headers are capped, bodies per operation; the JSON and XML decoders bound sizes, and the CBOR decoder also bounds nesting depth.
- **Secrets in logs.** Members AWS's models mark sensitive, signatures, session tokens and minted credentials are redacted from logs and from the journal. Secret values and KMS plaintext never appear in error messages.
- **Containers.** Function containers run with every capability dropped, `no-new-privileges`, memory and process limits, read-only code and no Docker socket. Every container understudy starts is labeled and removed when it stops, and leftovers of a crashed run are swept.
- **No outbound traffic.** understudy sends no telemetry and makes no network calls at runtime except Docker image pulls. It starts DynamoDB Local with that program's telemetry turned off.
- **Cryptography.** KMS uses AES-256-GCM with key material from `crypto/rand`, even when identifiers are seeded.
- **Sealing in this repository.** `scripts/sealed` must never let a command reach a non-loopback endpoint or read real AWS credentials.

## What understudy does not protect, by design

- **Anyone who can reach the port has full access.** Request signatures are parsed, not verified, by default; under `--sigv4 verify` every client may still sign with the public dummy pair `test`/`test`. IAM, resource and key policies are stored, not enforced. Any client that reaches the AWS API can read every secret and object and run any container image as a Lambda function.
- **The Docker socket is root on the host.** understudy needs it for Lambda and DynamoDB Local; so does anyone who controls understudy. Mounting it into the understudy container grants the same.
- **Function code is trusted.** Lambda functions run your code in containers on your Docker daemon. The Lambda Runtime API has no authentication in AWS's protocol; each environment gets its own port on the container network. On a Linux host, functions reach only the AWS API and the read-only health and clock endpoints. When understudy runs in a container, or on Docker Desktop, function code can also reach the rest of the admin API, so it can reset state, change the clock or read the journal. To close that, start understudy with `UNDERSTUDY_ADMIN_TOKEN` (and `--allow-unauthenticated-aws-api`, since a token puts the listener under the host rules) and give the token only to your own clients; function code then gets `401` from every admin path, health and clock included, because in these topologies it calls the main listener, where every admin call needs the token. Run only function code you trust.
- **Archives hold everything.** A snapshot, or the `--persist` directory, contains every stored secret and object together with the KMS key material that encrypts them. Treat them as secrets.
- **DynamoDB Local and the Lambda base images** are AWS's software, pulled at runtime under their own terms. understudy pins them by digest but does not audit them.
