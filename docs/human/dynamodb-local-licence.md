# DynamoDB Local licence: brief for counsel

For the repository owner, to take to counsel. This covers ARCHITECTURE.md open decisions 25.2 (may understudy keep serving DynamoDB through AWS's DynamoDB Local) and 25.6 (may the project accept the licence for its own CI). Both must be settled before the first public tag. The research behind this page, with every source, is `docs/research/phase1/dynamodb-local-terms.md` (fetched 2026-10-08). Nothing here is legal advice.

## 1. The licence text

DynamoDB Local ships under the "Amazon DynamoDB Local License Agreement", a file named `LICENSE.txt`. The research found it byte-identical (SHA-256 starting `16d139a35a9f`, 9450 bytes) in every distribution: the Docker Hub image understudy pins (`amazon/dynamodb-local@sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab`, version 3.3.1, at `/home/dynamodblocal/LICENSE.txt`), the ECR Public mirror, the download zips and the Maven jars 3.0.0 to 3.3.1. Check the text yourself without Docker before you send it:

```sh
curl -fsSLO https://repo1.maven.org/maven2/software/amazon/dynamodb/DynamoDBLocal/3.3.1/DynamoDBLocal-3.3.1.jar
unzip -p DynamoDBLocal-3.3.1.jar LICENSE.txt > DynamoDBLocal-LICENSE.txt
shasum -a 256 DynamoDBLocal-LICENSE.txt     # expect 16d139a35a9f...
wc -c DynamoDBLocal-LICENSE.txt             # expect 9450
```

Send counsel the whole file. The clauses that matter, verbatim except that typographic quotes are normalised to ASCII (the source's own spelling is kept):

**Preamble.**

> THIS LICENSE AGREEMENT ("LICENSE") GOVERNS YOUR USE OF THE DYNAMODB LOCAL SOFTWARE (TOGETHER WITH ANY UPDATES AND ENHANCEMENTS TO IT, AND ACCOMPANYING DOCUMENTATION, THE "SOFTWARE"). IF YOU INSTALL OR USE THE SOFTWARE, YOU WILL BE BOUND BY THIS LICENSE. UNLESS OTHERWISE DEFINED IN THIS LICENSE, CAPTIALIZED TERMS WILL HAVE THE SAME MEANING AS SET FORTH IN THE AWS CUSTOMER AGREEMENT POSTED AT AWS.AMAZON.COM/AGREEMENT (THE "AWS AGREEMENT").

**License (the grant and eligibility).**

> AWS Licensor or its licensors own all right, title, and interest in and to the Software and all related technology and intellectual property rights. Subject to the terms of this License and the AWS Agreement, AWS Licensor hereby grants you a personal, limited, nonexclusive, non-transferable, non-sublicenseable license to (a) install the Software on computer equipment owned or controlled by you and (b) use the Software solely (i) for your internal business purposes and (ii) in connection with the Services. You may not use the Software if you do not have an AWS account in good standing. [...]

**Limitations.**

> You may not, and you will not encourage, assist, or authorize any other person to, (a) incorporate any portion of it into your own programs or compile any portion of it in combination with your own programs; or (b) sell, rent, lease, lend, loan, distribute, act as a service bureau, publicly communicate, transform, or sub-license the Software or otherwise assign any rights to the Software in whole or in part; (c) modify, alter, tamper with, repair, or otherwise create derivative works of the Software, or (d) reverse engineer, disassemble, or decompile the Software or apply any other process or procedure to derive the source code of any software included in the Software. All rights granted to you are conditioned on your continued compliance this License, and will immediately and automatically terminate if you do not comply with any term or condition of this License or the AWS Agreement, including any failure to remit timely payment for the Software or the Service.

**Termination and amendment.**

> [...] AWS Licensor may modify, suspend, discontinue, or terminate your right to use part or all of the Software at any time without notice to you, and in that event AWS Licensor may modify the Software to make it inoperable.

> AWS Licensor may amend this License at its sole discretion by posting the revised terms on the AWS website (aws.amazon.com) or within the Software. Your continued use of the Software after any amendment's effective date evidences your agreement to be bound by it.

Facts around the text that counsel will want:

- "Services" is defined in the AWS Customer Agreement (last updated August 14, 2026): "'Service' means each of the services made available by us or our affiliates, including those web services described in the Service Terms. Services do not include Third-Party Content." The licence does not define what "in connection with" requires.
- AWS Service Terms 1.1: "You may not transfer outside the Services any software (including related documentation) you obtain from us or third party licensors in connection with the Services without specific authorization to do so."
- The words "emulator", "third-party tool", "proxy", "wrapper" and "CI" appear nowhere in the licence. No AWS page the research read (Customer Agreement, Service Terms, IP License, Site Terms, the DynamoDB Local docs, both registry pages) says anything about third-party emulators using DynamoDB Local.
- AWS's own pages point the other way: the Docker Hub page invites you to "include DynamoDB local in your containerized builds and as part of your continuous integration testing"; the developer guide says "Download DynamoDB local for free" and "you don't need an internet connection while you develop your application", and tells users to supply fake credentials. Neither page mentions the account requirement, which appears only in `LICENSE.txt`.
- Precedent: LocalStack downloaded the zip at first use and rewrote the jar, with no licence notice found. Floci leaves running DynamoDB Local to the user. Nobody the research found prints the licence before the pull.

## 2. What understudy does with DynamoDB Local

From ADR 0014, ARCHITECTURE.md 15 and `internal/service/dynamodb/ddblocal`:

- **Nothing is bundled.** The binary and the understudy image contain no part of DynamoDB Local. Nothing is redistributed, re-tagged, saved or patched.
- **Nothing happens without consent.** understudy pulls the pinned image onto the user's own Docker daemon (Docker Hub first, ECR Public as fallback, same digest) only after the user starts it with `--accept-dynamodb-local-license` or `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1`. It never prompts and never accepts by default. Until the user accepts, every DynamoDB call answers 501 with this notice (`ddblocal.Notice()`), and every other service works:

```text
understudy runs the DynamoDB service on Amazon DynamoDB Local. understudy does not
include or redistribute it. It will be pulled onto this machine from a public registry:

  image    amazon/dynamodb-local@sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab
  licence  Amazon DynamoDB Local License Agreement (AWS, not Apache-2.0).
           After the pull it is at /home/dynamodblocal/LICENSE.txt inside the image.

That licence says, among other terms, that you may use the software "solely (i) for your
internal business purposes and (ii) in connection with the Services", and that "You may
not use the Software if you do not have an AWS account in good standing." AWS can change
or end those terms without notice.

If that does not describe you, do not enable the DynamoDB service. Every other
understudy service works without an AWS account.

understudy starts DynamoDB Local with its telemetry turned off. The image is pulled once;
after that DynamoDB Local needs no network access.

To accept, start understudy with --accept-dynamodb-local-license or set UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1.
```

- **Acceptance is recorded per image digest.** With `--persist`, understudy writes the acceptance to the data directory (`LoadLicence`), and every later run on that directory counts as accepted without the flag. A new pinned version asks again.
- **Go tests can accept in code.** The public `understudytest` package lets a user's own Go tests choose `DynamoDBAccepted`, which accepts on the test's behalf. The user has to pick it explicitly.
- **It runs the unmodified image.** The container starts with `-disableTelemetry` and `DDB_LOCAL_TELEMETRY=0`. A permanent test asserts that the container makes no network attempt. The container is labelled and removed at shutdown.
- **It talks to Local only over HTTP.** understudy never touches Local's files or classes. Each client request is validated, journaled and subject to fault injection, then its original bytes are forwarded. The credential scope is rewritten so that Local keeps each account and region apart. In Local's responses understudy rewrites ARNs, regions, some error texts and timestamps. It answers some operations itself and never forwards them: tags, TTL (expiry runs on understudy's clock) and point-in-time-recovery settings.
- **It also drives Local with calls of its own.** Not every call Local sees comes from a client. The TTL sweep scans each table that has TTL enabled and removes expired items with a conditional `DeleteItem`, timed by the emulator clock (`ttl.go`). After an `UpdateItem`, or an update inside a transaction, understudy reads the item back to learn its new expiry. Snapshot export reads every table, snapshot import recreates the tables and writes their items back, and reset deletes them (`snapshot.go`, `reset.go`). All three files are in `internal/service/dynamodb`.
- **The README says so.** It states that DynamoDB runs on DynamoDB Local, that Local is under its own licence, not Apache-2.0, and that the licence requires an AWS account in good standing.
- **Project scripts and tests accept only when told to.** Every conformance script (`conformance/*/run.sh`) makes the person running it choose: exactly one of `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1` (accept and run DynamoDB's tests) and `CONFORMANCE_DYNAMODB=0` (skip them, each skip listed in the run's summary) must be set, and with neither or both it exits 2 before building anything. Under the opt-out the `e2e-pipeline` suite still runs, skipping the tests that make a worker take a DynamoDB lock or read the lock table. The docker-tagged Go tests that run Local (`internal/service/dynamodb/docker_test.go`, `snapshot_docker_test.go`, `ddblocal/egress_docker_test.go` and `internal/server/shutdown_docker_test.go`) skip without the same variable, so `just test-docker` runs Local only for a caller who set it. The phase 1 release review ran the suites with the licence accepted on the developer's own machine.
- **Project CI does not accept.** Neither CI job that could run Local sets `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE`: the `test-docker` job's DynamoDB tests skip, and the `conformance` job runs all six suites with `CONFORMANCE_DYNAMODB: "0"`, which skips their DynamoDB tests, and in `e2e-pipeline` the tests that depend on the workers' DynamoDB lock. `docs/plan/PHASE1-REPORT.md` finding 2 says the Python and e2e suites accept "in CI"; that described the scripts at the time of the review, which accepted unconditionally.

## 3. Questions for counsel

1. **Users without an AWS account.** A user who has no AWS account and passes the flag breaches "You may not use the Software if you do not have an AWS account in good standing". Does any of that fall on the project for automating the pull?
2. **"In connection with the Services".** Does testing code locally that is meant to run on AWS count as use "in connection with the Services" when the workflow calls no AWS service? Does a user who never deploys to AWS fall outside the grant?
3. **Installation by understudy.** Does understudy pulling the image onto the user's Docker daemon satisfy "install the Software on computer equipment owned or controlled by you"? CI runners owned by a third party, such as GitHub-hosted runners, are a separate case.
4. **The Limitations clause.** Does a proxy that forwards requests to Local over HTTP, rewrites its responses, partitions it by account, injects faults, and drives it with its own calls (the TTL sweep, snapshot import and reset) "incorporate any portion of it into your own programs", "act as a service bureau", "transform" the Software, or create "derivative works"? Does shipping that automation "encourage, assist, or authorize any other person" to do any of these?
5. **AWS's own statements.** Do the Docker Hub page and the developer guide (free download, CI use, no internet needed, fake credentials) affect the answers to 1 to 4?
6. **The gate.** Is the current notice plus an explicit flag sufficient? Under `--persist` the acceptance is already stored per image digest and covers later runs without the flag. Is that stored acceptance enough, or is more needed (an interactive acceptance, a record of who accepted and when, different wording)? Please review the notice text in section 2.
7. **Scripts and tests that accept for the user.** The conformance scripts and the docker-tagged Go tests now accept only when the person running them sets `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1`. Is that explicit opt-in enough, as it is for a user of `understudytest.DynamoDBAccepted`?
8. **Project CI (25.6).** The committed workflow accepts the licence nowhere (section 2). The developer has accepted it locally for the release review. May the project accept for its CI at all? If it may, whose AWS account in good standing covers it, and is an open-source project's CI an "internal business purpose"? Does that also cover the conformance job running DynamoDB for every suite?

While counsel is engaged, two other licence questions are open: the terms of the Lambda base images understudy pulls from `public.ecr.aws/lambda/*` (never examined; `dynamodb-local-terms.md` 6), and whether AWS managed policy documents may be fetched and shipped (ARCHITECTURE.md 25.5).

## 4. What each answer leads to

- **Until counsel answers.** Nothing to do: CI accepts nowhere, and every script and test accepts only for a caller who sets `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1`. DynamoDB conformance stays a human run.
- **Yes, as built.** Close 25.2 with an ADR 0014 amendment that records the reading, then decide 25.6. Nothing in the code changes.
- **Yes, with conditions.** The places to change:
  - the notice text (`Notice()` in `internal/service/dynamodb/ddblocal/licence.go`)
  - the README's DynamoDB section
  - the acceptance mechanism (`LoadLicence` in the same file)
  - the test helper (`DynamoDBAccepted` in `understudytest/understudytest.go`)
  - the conformance scripts' licence gates (`conformance/*/run.sh`)
  - the docker-tagged tests' licence gate (`configtest.DynamoDBLicence` in `internal/config/configtest`)
  - the CI workflow (`.github/workflows/ci.yml`: the `test-docker` job and its comment, and the `conformance` job's `CONFORMANCE_DYNAMODB`)
- **No.** Build the fallback in section 5 before the tag, or ship the tag with DynamoDB disabled and say so in the README.

## 5. The fallback if the answer is no

This is designed but not built (ARCHITECTURE.md 25.2, ADR 0014 review amendment, `docs/design/reviews/product-risk.md` M4). It is a native implementation of the phase 1 lock-table workload: `CreateTable`, `PutItem`, `GetItem`, `DeleteItem`, `UpdateItem` and `Query` with condition expressions, plus TTL. It sits behind the same generated `dynamodbapi` package and the same `RawHandler` seam Local uses today. Validation, the journal, fault injection, the access recorder and the shim-owned state (tags, TTL on the emulator clock, point-in-time-recovery settings, per account and region isolation) stay as they are. Only the backend behind the seam changes.

What it costs:

- Today 26 DynamoDB operations are bound: 6 full, 2 stored-only, 18 partial (`docs/coverage.md`). Under the fallback, `Scan`, batch operations, transactions and PartiQL would answer 501 until someone builds them.
- The designed subset names no `DescribeTable`, `DeleteTable` or `ListTables`. Terraform's `aws_dynamodb_table` describes and deletes the table, and `aws dynamodb wait table-exists` polls `DescribeTable`. Keeping brief item 6 (Terraform-first) for DynamoDB means adding those three to the subset.
- No AWS account and no image pull would be needed for DynamoDB, which removes the exception to brief item 9 ("no account").

If counsel allows use by users who accept for themselves but not by the project's CI, both backends can coexist: the native subset as the default, and Local as the opt-in it is today. That choice is the owner's, and it goes into an ADR 0014 amendment.
