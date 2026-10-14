# Licensing: precedent and decisions for 25.2, 25.5, 25.6 and the Lambda images

Answers the questions in `docs/human/dynamodb-local-licence.md` sections 3 and 4 (the brief for counsel, removed with the licence gate on 2026-10-14; read it at commit 58e869d), ARCHITECTURE.md 25.2, 25.5 and 25.6 (`docs/design/ARCHITECTURE.md:1466-1470`), and the two side questions in `docs/plan/HUMAN-ACTIONS.md:12-14`. The owner will not engage counsel, so each section ends in a decision the project acts on. Research date: 2026-10-14. Builds on [dynamodb-local-terms.md](dynamodb-local-terms.md), which holds the full DynamoDB Local licence text and the AWS terms as of 2026-10-08; those are cited by line and not repeated. No AWS API was called.

## Evidence labels

- **SOURCE**: quoted from the cited document as fetched on 2026-10-14.
- **OBSERVED**: run here on 2026-10-14 (Docker, linux/amd64, container labelled `understudy.dev/test=1`, `--network none`, removed afterwards).
- **INTERPRETATION**: a reading of a SOURCE. Not legal advice. It is the reading the project adopts.

Repositories are pinned at the commits read: [awslabs/mcp@8840003](https://github.com/awslabs/mcp/tree/8840003820a78dd018561d55c7ab12fcdd708464), [getmoto/moto@c5f0579](https://github.com/getmoto/moto/tree/c5f05794a3d779ee6a781d3b1a858911d64484c4), [laravel/framework@0fd7388](https://github.com/laravel/framework/tree/0fd73881deb1d659d39e975c814fd35df2cea6f8), [awslabs/amazon-kinesis-client@2a011af](https://github.com/awslabs/amazon-kinesis-client/tree/2a011af382fc261c51a21e0bb8737cc854aab670), [aws/aws-lambda-base-images@912a005](https://github.com/aws/aws-lambda-base-images/tree/912a0051a3a89b8c89c3c750b43068ff67e8425b), [iann0036/iam-dataset@9aa5d81](https://github.com/iann0036/iam-dataset/tree/9aa5d819b2671e2d9c5b58fbe518bc1fee8c45f9). Licences and push dates come from the GitHub repository API (`api.github.com/repos/<owner>/<repo>`).

## Summary

| Question | Decision |
|---|---|
| 25.2: pull and proxy DynamoDB Local for users who opt in | Yes, as built. Close 25.2 in an ADR 0014 amendment. |
| 25.6: accept the licence in the project's CI, including fork PRs | Yes, with the owner as the named licensee. Set `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1` on the jobs that need it. Never cache or push the image. |
| 25.5: ship a catalogue of AWS managed policy documents | Yes, sourced from the AWS Managed Policy Reference Guide through its published `llms.txt` and `.md` pages. A maintainer runs the generator; users never do. Ship it as a separately licensed data directory (MIT-0, with CC BY-SA 4.0 attribution kept as well). Do not use the IAM API or third-party datasets. |
| Lambda base images (`public.ecr.aws/lambda/*`) | No restriction found. The image source repo is Apache-2.0, the layers carry only open-source licences, and AWS documents local `docker run`. Pull and run freely, locally and in CI. Do not re-host the images. |

Outcome (2026-10-14): the owner went one step further than sections 1 and 2 and dropped the acceptance gate altogether. DynamoDB now runs by default, and DynamoDB Local's first start logs a one-line notice with the licence URL. CI therefore needs no acceptance variable. Every constraint below still holds (ADR 0014 amendment of 2026-10-14).

---

## 1. DynamoDB Local at runtime (25.2)

### Facts

- The grant is "personal, limited, nonexclusive, non-transferable" to "install the Software on computer equipment owned or controlled by you and ... use the Software solely (i) for your internal business purposes and (ii) in connection with the Services", and "You may not use the Software if you do not have an AWS account in good standing" (SOURCE, `dynamodb-local-terms.md:78`).
- Limitations: no "incorporate any portion of it into your own programs or compile any portion of it in combination with your own programs"; no "distribute, act as a service bureau, publicly communicate, transform, or sub-license"; no "modify ... or otherwise create derivative works"; and you will not "encourage, assist, or authorize any other person to" do these (SOURCE, `dynamodb-local-terms.md:82`).
- AWS may change or end the licence at any time (SOURCE, `dynamodb-local-terms.md:92-94`).
- AWS's own distribution pages invite container and CI use and say nothing of the account requirement. The Hub page says "include DynamoDB local in your containerized builds and as part of your continuous integration testing". The docs say "Download DynamoDB local for free" and tell users to supply fake credentials (SOURCE, `dynamodb-local-terms.md:109`).
- None of the Customer Agreement, Service Terms, IP License, Site Terms or the DynamoDB Local docs mentions third-party emulators or wrappers (SOURCE, by absence, `dynamodb-local-terms.md:211`).
- understudy bundles nothing. It pulls the pinned image unmodified only after an explicit flag or environment variable, prints the account requirement first, and talks to Local only over HTTP (`docs/human/dynamodb-local-licence.md:48-78`, ADR 0014 `docs/adr/0014-dynamodb-local-proxy.md:11-12`).

### Precedent

- **AWS's own tool does exactly this, with less of a gate.** The AWS-authored, Apache-2.0 DynamoDB MCP server ([awslabs/mcp](https://github.com/awslabs/mcp/tree/8840003820a78dd018561d55c7ab12fcdd708464/src/dynamodb-mcp-server)) starts DynamoDB Local on the user's machine automatically for its `dynamodb_data_model_validation` tool. It runs `docker run -d ... amazon/dynamodb-local` ([model_validation_utils.py:39](https://github.com/awslabs/mcp/blob/8840003820a78dd018561d55c7ab12fcdd708464/src/dynamodb-mcp-server/awslabs/dynamodb_mcp_server/model_validation_utils.py#L39), [:605-627](https://github.com/awslabs/mcp/blob/8840003820a78dd018561d55c7ab12fcdd708464/src/dynamodb-mcp-server/awslabs/dynamodb_mcp_server/model_validation_utils.py#L605-L627)). Without a container runtime it downloads and extracts the jar itself ([:370-412](https://github.com/awslabs/mcp/blob/8840003820a78dd018561d55c7ab12fcdd708464/src/dynamodb-mcp-server/awslabs/dynamodb_mcp_server/model_validation_utils.py#L370-L412)). The only "licen" matches in that file are the Apache header, and its README has none: it shows no licence notice and asks for no acceptance (SOURCE, grep of both files). Then it drives Local with its own calls (creating tables, loading data, running access patterns), as understudy's TTL sweep and snapshot code do.
- **LocalStack** downloaded the zip at first use, then rewrote the jar and injected a javaagent. It showed no licence notice (`dynamodb-local-terms.md:219`). It ran that way for years, and no AWS objection was found.
- **testcontainers** modules wrap `amazon/dynamodb-local` in Go, .NET and Rust (all MIT): [testcontainers-go modules/dynamodb](https://github.com/testcontainers/testcontainers-go/blob/main/modules/dynamodb/dynamodb_test.go), [Testcontainers.DynamoDb](https://github.com/testcontainers/testcontainers-dotnet/blob/develop/src/Testcontainers.DynamoDb/DynamoDbBuilder.cs), [testcontainers-rs dynamodb_local](https://github.com/testcontainers/testcontainers-rs-modules-community/blob/main/src/dynamodb_local/mod.rs). Each pulls and starts the image on the user's behalf with no licence gate.
- **AWS Powertools for .NET** (AWS-maintained, MIT-0) starts `amazon/dynamodb-local:latest` from test fixtures ([DynamoDBFixture.cs](https://github.com/aws-powertools/powertools-lambda-dotnet/blob/develop/libraries/tests/AWS.Lambda.Powertools.Idempotency.Tests/Persistence/DynamoDBFixture.cs)).
- **AWS enforcement on GitHub.** The public DMCA record ([github/dmca](https://github.com/github/dmca)) has AWS notices for leaked internal code ([2013](https://github.com/github/dmca/blob/master/2013/2013-06-12-Amazon-Web-Services.md), [2015](https://github.com/github/dmca/blob/master/2015/2015-04-03-aws.md)), a paid study guide ([2018](https://github.com/github/dmca/blob/master/2018/2018-07-04-Amazon.md)) and redistributed copies of Lumberyard, whose terms limited where forks could be distributed ([2017](https://github.com/github/dmca/blob/master/2017/2017-11-17-AWS.md)). Code searches of that repository for `dynamodb` and `DynamoDBLocal` found no notice about DynamoDB Local (SOURCE, `gh api search/code`, 2026-10-14). Every AWS notice found targets copies of AWS material. None targets tools that fetch AWS software for a user.

### Reading of the counsel questions (INTERPRETATION)

1. **A user without an AWS account** breaches a condition that binds the user, who is the licensee. understudy is not a party to the licence, and the notice tells the user the condition before anything is pulled. That is the opposite of "encourage". The account condition is the user's to meet, as it is for every testcontainers or AWS MCP user.
2. **"In connection with the Services"** is undefined. AWS's own pages describe local development and CI for code that targets DynamoDB as the intended use (`dynamodb-local-terms.md:109`). Testing code meant for AWS is the use AWS advertises.
3. **"Installed by understudy"**: the pull runs on the user's Docker daemon, at the user's command, on equipment the user controls. testcontainers and the AWS MCP server do the same.
4. **Limitations.** understudy is an HTTP client of an unmodified program. Clause (a) is about incorporating or compiling code. understudy copies no code, links none, ships none, and the image runs byte-identical. A client that reshapes responses for its own caller does not "transform" or create a derivative work of the server, as an SDK that parses DynamoDB Local's output does not. "Service bureau" means operating the software for third parties. A per-user local process is not that, but a hosted multi-tenant understudy would be.
5. **AWS's own statements** weigh toward permission: free download, CI use, no internet needed, fake credentials, and AWS's own MCP server automating the pull with no gate.
6. **The gate** is already stricter than any precedent found: an explicit flag, the account clause quoted, acceptance stored per digest, no prompt, no default.

### Decision

Keep 25.2 as built. Record this reading in an ADR 0014 amendment and close 25.2. Keep the native lock-table fallback designed but unbuilt (`docs/human/dynamodb-local-licence.md:111-119`).

### Residual risk and how to keep it low

- AWS can amend or revoke the licence unilaterally (`dynamodb-local-terms.md:92-94`). The worst plausible outcome is a request to stop, not damages: the licence caps AWS's liability, not the user's, and the project ships nothing of AWS's. Response plan: on any AWS request, disable the DynamoDB service by default in a patch release and build the fallback.
- Hold the lines that separate understudy from the Lumberyard pattern. Never bundle, mirror, re-tag, `docker save`, push or patch the image. Never touch Local's files or classes. Never offer understudy as a hosted multi-user service with Local behind it, and say so in the README's DynamoDB section.
- Keep the notice quoting the account clause, and keep README wording that DynamoDB is the one service that needs an AWS account.

---

## 2. DynamoDB Local in the project's CI (25.6)

### Facts

- The licensee is whoever installs or uses the Software (`dynamodb-local-terms.md:74`). For a CI job, that is the project that authors and runs the workflow. A fork contributor triggers the run but does not choose its terms.
- Today CI accepts nowhere: `test-docker` skips the DynamoDB tests, and `conformance` sets `CONFORMANCE_DYNAMODB: "0"` (`.github/workflows/ci.yml:106-111`, `.github/workflows/ci.yml:164-174`). The workflow runs on `push` to main and on every `pull_request` (`.github/workflows/ci.yml:3-7`).
- GitHub-hosted runners are ephemeral VMs that the job controls for its duration. The licence asks for equipment "owned or controlled by you" (`dynamodb-local-terms.md:78`).

### Precedent

- **AWS runs DynamoDB Local in its own public CI on PRs from forks.** In awslabs/amazon-kinesis-client (Apache-2.0), unit tests start `DynamoDBEmbedded`, the in-process DynamoDB Local ([CoordinatorStateDAOTest.java](https://github.com/awslabs/amazon-kinesis-client/blob/2a011af382fc261c51a21e0bb8737cc854aab670/amazon-kinesis-client/src/test/java/software/amazon/kinesis/coordinator/CoordinatorStateDAOTest.java)), with `DynamoDBLocal` as a test dependency ([pom.xml](https://github.com/awslabs/amazon-kinesis-client/blob/2a011af382fc261c51a21e0bb8737cc854aab670/amazon-kinesis-client/pom.xml)). Its workflow runs `mvn -B package` on `pull_request` with GitHub-hosted `ubuntu-latest` runners ([maven.yml:10-37](https://github.com/awslabs/amazon-kinesis-client/blob/2a011af382fc261c51a21e0bb8737cc854aab670/.github/workflows/maven.yml#L10-L37)). `package` runs the unit tests, and the `-DskipITs` flag skips only integration tests. aws-dynamodb-encryption-java does the same with `DynamoDBLocal` in test scope ([sdk1/pom.xml](https://github.com/aws/aws-dynamodb-encryption-java/blob/master/sdk1/pom.xml)).
- **Large OSS projects run the image as a service container on every PR.** laravel/framework does ([tests.yml:3-10, 45-48](https://github.com/laravel/framework/blob/0fd73881deb1d659d39e975c814fd35df2cea6f8/.github/workflows/tests.yml#L3-L48): `pull_request` trigger, `amazon/dynamodb-local:2.0.0` service). So do treeverse/lakeFS ([esti.yaml](https://github.com/treeverse/lakeFS/blob/master/.github/workflows/esti.yaml), `amazon/dynamodb-local:3.3.0`), Budibase (pinned by digest, [budibase_ci.yml](https://github.com/Budibase/budibase/blob/master/.github/workflows/budibase_ci.yml)), archtechx/tenancy, mockery, Carbon and dbgate (grep.app search for `amazon/dynamodb-local` under `.github/workflows`).
- **A Marketplace action exists for it.** [rrainn/dynamodb-action](https://github.com/rrainn/dynamodb-action) (MIT, created 2019-11-03) is described as "Starts DynamoDB Local in background as a Docker container for support with GitHub Actions" ([action.yml](https://github.com/rrainn/dynamodb-action/blob/main/action.yml)).
- AWS's Hub page names CI as an intended use (`dynamodb-local-terms.md:109`).
- No takedown, issue or AWS statement against any of these was found (section 1, DMCA search).

### Decision

Accept the licence in CI. The owner, who holds an AWS account in good standing, is the named licensee.

- Set `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1` on `test-docker` and on every `conformance` matrix entry, and drop `CONFORMANCE_DYNAMODB: "0"`. Replace the comments at `.github/workflows/ci.yml:108-111` and `:164-170` with one sentence: the project owner accepts the Amazon DynamoDB Local License Agreement for this workflow (ARCHITECTURE.md 25.6).
- Fork PRs run it too, as in amazon-kinesis-client and Laravel. Keep the trigger `pull_request` (never `pull_request_target`), so fork runs get no secrets and a read-only token. Acceptance is a plain environment variable, not a secret.
- Close 25.6 in the same ADR 0014 amendment as 25.2, and update `docs/plan/HUMAN-ACTIONS.md` item 1 and `docs/human/dynamodb-local-licence.md` section 2 ("Project CI does not accept").

### Residual risk and how to keep it low

- "Internal business purposes" for an open-source project is untested. The reading is that testing one's own software is internal, and AWS's own OSS repos rely on the same reading.
- Pull by the pinned digest only. Do not put the image in `actions/cache` or any registry, and never upload Local's files as artifacts, so CI never "distributes". Keep both telemetry opt-outs and the egress test.
- If the owner's AWS account lapses, set `CONFORMANCE_DYNAMODB=0` again until it is restored.

---

## 3. AWS managed policy documents (25.5)

### Facts: what the terms say

- AWS Site Terms (last updated June 4, 2025; [aws.amazon.com/terms](https://aws.amazon.com/terms/)), "License and Site Access", verbatim (SOURCE):
  - "AWS grants you a limited license to access and make personal use of the AWS Site and not to download (other than page caching) or modify it, or any portion of it, except with express written consent of AWS. This license does not include ... any use of data mining, robots, or similar data gathering and extraction tools."
  - In the same paragraph: "Unless otherwise specified by AWS in a separate license, your right to use any software, data, documentation or other materials that you access or download through the AWS Site is subject to these Site Terms or, if you have an AWS account, the Agreement. The materials hosted on docs.aws.amazon.com are licensed as follows: documentation (e.g., user guides, developer guides, other publications) is licensed under CC-BY-SA-4.0, while any code therein is licensed under MIT-0."
- So the content licence (CC BY-SA 4.0 for prose, MIT-0 for code) is explicit, and so is the access restriction (no robots). The two govern different things: what may be done with the material, and how the site may be accessed (INTERPRETATION).
- CC BY-SA 4.0 ([legalcode](https://creativecommons.org/licenses/by-sa/4.0/legalcode.txt), SOURCE):
  - The grant is "irrevocable" (s. 2(a)(1)).
  - "simply making modifications authorized by this Section 2(a)(4) never produces Adapted Material" (s. 2(a)(4), media and formats; technical modifications).
  - Sharing requires attribution, a licence notice, a URI to the material and an indication of modifications (s. 3(a)(1)).
  - ShareAlike attaches only to Adapted Material (s. 3(b)).
  - For databases, "the database in which You have Sui Generis Database Rights (but not its individual contents) is Adapted Material" (s. 4(b)).
- AWS's archived documentation repos carry the same split: "The documentation is made available under the Creative Commons Attribution-ShareAlike 4.0 International License ... The sample code within this documentation is made available under a modified MIT license" ([amazon-dynamodb-developer-guide LICENSE-SUMMARY](https://github.com/awsdocs/amazon-dynamodb-developer-guide/blob/master/LICENSE-SUMMARY)). [awsdocs/iam-user-guide](https://github.com/awsdocs/iam-user-guide) is archived, and GitHub reports its licence as MIT-0. No awsdocs repository exists for the Managed Policy Reference Guide (`api.github.com/repos/awsdocs/aws-managed-policy` returns 404).
- The Customer Agreement IP License allows AWS Content only "in connection with your permitted use of the Services". It forbids you to "modify, distribute ... or otherwise create derivative works of any Content included in the Services or AWS Content" (SOURCE, `dynamodb-local-terms.md:196`). IAM API output reaches you as a customer under that agreement.

### Facts: what AWS publishes for machine access (SOURCE, fetched 2026-10-14)

- Each policy page shows the policy document under a "JSON policy document" heading as a code block, for example [AWSLambdaBasicExecutionRole](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/AWSLambdaBasicExecutionRole.html). The page also has a "View a markdown version of this page" link to [AWSLambdaBasicExecutionRole.md](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/AWSLambdaBasicExecutionRole.md) (`text/markdown`), which has the ARN, version id, creation and edit times, and the JSON in a fenced block.
- AWS publishes [docs.aws.amazon.com/llms.txt](https://docs.aws.amazon.com/llms.txt). It opens: "This file provides structured access to AWS service documentation for large language models". The guide has its own [llms.txt](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/llms.txt) listing 1,605 `.md` pages, one per policy plus the overview.
- [docs.aws.amazon.com/robots.txt](https://docs.aws.amazon.com/robots.txt) addresses `User-agent: *` and sets `Crawl-delay: 5`. It disallows only legacy API versions, `/search/`, `/forms/`, `/ask-docs/` and a few archives. `/aws-managed-policy/` is not disallowed.

### Sources compared

| Source | Licence of the content | Problem |
|---|---|---|
| Managed Policy Reference Guide (`.md` via `llms.txt`) | CC BY-SA 4.0; the JSON is "code therein", so MIT-0 | The Site Terms forbid "robots" in the same paragraph that grants the licence. That is mitigated by AWS's own `llms.txt` and by robots.txt permitting the path. |
| IAM API (`ListPolicies Scope=AWS`, `GetPolicyVersion`) | AWS Content under the Customer Agreement; the IP License forbids distribution | Needs an AWS account and credentials. On its face, the worst of the three. |
| botocore / aws-sdk data | Apache-2.0 | Has no managed policy documents. These packages hold service models; grep.app found no `AWSLambdaBasicExecutionRole` in boto/botocore. |
| [iann0036/iam-dataset](https://github.com/iann0036/iam-dataset) (MIT) | MIT covers the compilation only | Has `aws/managed_policies.json` (11 MB) and `aws/managedpolicies/`. How they are generated was not inspected. AWS's terms still govern each document. |
| [z0ph/MAMIP](https://github.com/z0ph/MAMIP) (GPL-3.0) | GPL-3.0 on the compilation | GPL-3.0 data inside an Apache-2.0 binary invites a copyleft dispute. |
| [salesforce/policy_sentry](https://github.com/salesforce/policy_sentry) (MIT), [duo-labs/parliament](https://github.com/duo-labs/parliament) (BSD-3-Clause), [cloudsplaining](https://github.com/salesforce/cloudsplaining) (BSD-3-Clause), [pacu](https://github.com/RhinoSecurityLabs/pacu) (BSD-3-Clause) | n/a | They ship `iam-definition.json` scraped from the Service Authorization Reference on docs.aws.amazon.com ([iam-dataset README:29-31](https://github.com/iann0036/iam-dataset/blob/9aa5d819b2671e2d9c5b58fbe518bc1fee8c45f9/README.md?plain=1#L29-L31) says it is "A scraping of the AWS Service Authorization Reference ... generated using work from Parliament"). That is precedent for scraping docs.aws.amazon.com and shipping the result under a permissive licence. None of them was found shipping managed policy documents. |

### Precedent

- **moto (Apache-2.0) has embedded every AWS managed policy document since 2017** and refreshes them monthly. The data was first added in commit [8f2f7fa](https://github.com/getmoto/moto/commit/8f2f7fa20da8), 2017-09-19, "Adding a script to import all of AWS' managed policies". [scripts/update_managed_policies.py](https://github.com/getmoto/moto/blob/c5f05794a3d779ee6a781d3b1a858911d64484c4/scripts/update_managed_policies.py) calls `list_policies(Scope="AWS")` and `get_policy_version` and writes [moto/iam/aws_managed_policies.py](https://github.com/getmoto/moto/blob/c5f05794a3d779ee6a781d3b1a858911d64484c4/moto/iam/aws_managed_policies.py) (4.7 MB). A scheduled workflow assumes a role in an AWS account, runs the script and opens a PR on the first of each month ([data-update_iam-managed-policies.yml:10-49](https://github.com/getmoto/moto/blob/c5f05794a3d779ee6a781d3b1a858911d64484c4/.github/workflows/data-update_iam-managed-policies.yml#L10-L49)). The latest such commit is 2026-10-05. That is the riskiest source (IAM API output under the Customer Agreement), redistributed on PyPI for nine years, with no objection found.
- iam-dataset (MIT) publishes all managed policies and lists LocalStack among its users ([README:13](https://github.com/iann0036/iam-dataset/blob/9aa5d819b2671e2d9c5b58fbe518bc1fee8c45f9/README.md?plain=1#L13)). MAMIP has archived every change since 2019 ([repo](https://github.com/z0ph/MAMIP)). No DMCA notice against either was found (section 1, DMCA search; `"managed polic"` returned 0 hits).

### Decision

Ship the catalogue, sourced from the Managed Policy Reference Guide. This supersedes the "blocked" note in `docs/plan/DEFERRED.md:163` and closes 25.5.

- **Licence position.** Treat each policy's JSON document as "code therein", which makes it MIT-0. Also comply with CC BY-SA 4.0 s. 3(a) for the page metadata (name, ARN, version, times) and as a safety net for the JSON. Converting the documents to a Go-embeddable file is a format change under s. 2(a)(4), so it produces no Adapted Material. Even if it did, ShareAlike would bind only the data directory, never the Apache-2.0 code beside it.
- **Layout.** Keep the data in `third_party/aws-managed-policies/` with:
  - its own `LICENSE`: the MIT-0 text, plus a CC BY-SA 4.0 notice for the documentation-derived fields;
  - a `NOTICE` with the attribution: "AWS managed policy documents from the AWS Managed Policy Reference Guide, https://docs.aws.amazon.com/aws-managed-policy/latest/reference/, (c) Amazon Web Services, Inc. or its affiliates, licensed under MIT-0 (code) and CC BY-SA 4.0 (documentation) per https://aws.amazon.com/terms/; reformatted to JSON, no other changes";
  - a per-policy source URL and sha256.
- **Binary.** The binary embeds the directory with `go:embed`. List the directory with its licences in the root `NOTICE`, and mention it in the README licence section.
- **Fetching.** A maintainer-run generator under `internal/cmd` reads only the guide's `llms.txt`, then the `.md` pages it lists. It obeys robots.txt and `Crawl-delay: 5` (about 2.2 hours for 1,605 pages), sends a descriptive User-Agent with the repository URL, and refetches only on a release cadence. Users' binaries never fetch anything.
- **Not used.** The IAM API and the third-party datasets. At most, iam-dataset may serve as an offline cross-check in tests that is never shipped.

### Residual risk and how to keep it low

- The literal "no robots" clause. Mitigations: AWS's own `llms.txt` invites machine access to this exact guide. Robots.txt permits the path. Fetch volume is one polite crawl per release. The licence on material already fetched is irrevocable (CC BY-SA 4.0 s. 2(a)(1)). If AWS objects to the fetching, stop the generator and keep the shipped data.
- Policy documents are short functional lists of action names. Whether they are copyrightable at all is doubtful. The decision does not rely on that.
- If AWS ever publishes the documents in a separately licensed bundle, switch to it.

---

## 4. Lambda base images (`public.ecr.aws/lambda/*`)

### Facts

- **ECR Public terms.** ECR Public lets publishers share content that "anyone with or without an AWS account ('Registry Users') can download and use", and "You may specify the terms under which you license Your Content to Registry Users" (Service Terms 80, SOURCE, `dynamodb-local-terms.md:205`). The terms therefore come from AWS as publisher. Unlike DynamoDB Local, no separate EULA was found.
- **Source repository.** The image source repository [aws/aws-lambda-base-images](https://github.com/aws/aws-lambda-base-images) is Apache-2.0. It holds one branch per image with the Dockerfile and the layer tarballs (for example `python3.13` has `Dockerfile.python3.13`, `LICENSE` (Apache-2.0), `NOTICE` "Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved." and `x86_64/*.tar.xz`). Its README ends "This project is licensed under the Apache-2.0 License" ([README](https://github.com/aws/aws-lambda-base-images/blob/912a0051a3a89b8c89c3c750b43068ff67e8425b/README.md)) (SOURCE).
- **Gallery text.** The ECR gallery text for `lambda/python` says "You can then locally test your function using the docker build and docker run commands" and gives `docker run -p 9000:8080 <image name>`. It states no licence terms (SOURCE, `getRepositoryCatalogData` for `lambda/python`).
- **Lambda docs.** "The AWS base images are preloaded with a language runtime, a runtime interface client ... and a runtime interface emulator for local testing" ([images-create.md:11](https://docs.aws.amazon.com/lambda/latest/dg/images-create.md)). The runtime interface emulator is Apache-2.0 ([aws-lambda-runtime-interface-emulator](https://github.com/aws/aws-lambda-runtime-interface-emulator)) (SOURCE).
- **Image contents** (OBSERVED, `public.ecr.aws/lambda/provided@sha256:a79fa3e8a59e153b5aa69323d10ee2c8f54826e9834afaa1ac4bc79fbaaaf39f`). Amazon Linux 2023 with `/usr/local/bin/aws-lambda-rie` and `/var/runtime/bootstrap`. The only licence file outside `/usr/share/licenses` is `/THIRD-PARTY-LICENSES.txt` (659,123 bytes). It holds open-source licences (Node.js MIT, Apache-2.0 for `awslambdaric` with an Amazon copyright NOTICE, and others). A search for "license agreement" and "customer agreement" found no AWS proprietary term in it. No file named `*eula*` exists.
- **Amazon Linux 2023.** "AL2023 images can be used outside of AWS", and "there is no additional charge" ([AL2023 FAQ](https://aws.amazon.com/linux/amazon-linux-2023/faqs/), SOURCE).

### Precedent

- aws-sam-cli (AWS, Apache-2.0) pulls `public.ecr.aws/lambda/<runtime>` and builds a local invoke image on top ([lambda_image.py](https://github.com/aws/aws-sam-cli/blob/develop/samcli/local/docker/lambda_image.py), `_INVOKE_REPO_PREFIX = "public.ecr.aws/lambda"`). Its integration tests do the same in CI.
- LocalStack executed functions in `public.ecr.aws/lambda/` images ([docker_runtime_executor.py](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/lambda_/invocation/docker_runtime_executor.py), `IMAGE_PREFIX = "public.ecr.aws/lambda/"`), and localstack-extensions CI pulls `public.ecr.aws/lambda/python:3.12` ([wiremock.yml](https://github.com/localstack/localstack-extensions/blob/main/.github/workflows/wiremock.yml)).
- No AWS objection was found.

### Decision

No gate and no notice. understudy and its CI pull and run these images freely, as they do today (`internal/service/lambda/launcher/launcher_test.go:31`, `internal/topology/detect_test.go:17`). Close the open item in `dynamodb-local-terms.md:300` and `docs/plan/HUMAN-ACTIONS.md:14`.

### Residual risk and how to keep it low

- Re-hosting the images (a mirror, a pushed derived image, a `docker save` artifact) would make the project a distributor of GPL components with source-offer duties. Pull from AWS's registry only. Function images that understudy builds locally on top of a base image stay on the user's daemon.
- AWS could add terms to a later image. Keep pins by digest, and recheck `/THIRD-PARTY-LICENSES.txt` and the gallery text when a pin moves.
