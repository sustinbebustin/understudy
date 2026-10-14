# DynamoDB Local: licence, telemetry and offline behaviour

Research item G5: closed for telemetry and offline behaviour; the licence reading still needs counsel. Date of research: 2026-10-08. Facts only. Nothing here is legal advice; every legal reading is marked INTERPRETATION and needs a lawyer before the first public release.

## Evidence labels

- **OBSERVED**: I ran it on 2026-10-08 (Docker 29.6.2, linux/amd64, no real AWS endpoint called, all containers labelled `understudy.dev/test=1` and removed afterwards).
- **SOURCE**: quoted from the cited document as fetched on 2026-10-08.
- **INTERPRETATION**: a reading of a SOURCE, not a fact.
- **UNVERIFIED**: could not be confirmed.

## Findings in one table

| Question | Answer | Label |
|---|---|---|
| Do Docker Hub, ECR Public and the Maven/zip distributions carry different licence text? | No. `LICENSE.txt` is byte-identical (SHA-256 `16d139a35a9f...`, 9450 bytes) in all of them, and across 3.0.0 to 3.3.1 and the v1.x, v2.x and v3.x zips. | OBSERVED |
| Are the two registries the same artifact? | Yes. Index digest, both platform manifests and both attestation manifests are identical. | OBSERVED |
| Does `DDB_LOCAL_TELEMETRY=0` alone disable telemetry in the Docker image? | Yes. Zero DNS queries and zero connections in 5 minutes. | OBSERVED |
| Does `-disableTelemetry` alone? | Yes. Same result. | OBSERVED |
| Do other values work (`false`, `FALSE`, `1`, `true`)? | No. All four still attempt telemetry. AWS's prose says "set ... to `false`"; only the literal `0` works. | OBSERVED |
| What does Local contact by default? | `cognito-identity.us-east-1.amazonaws.com` then `pinpoint.us-east-1.amazonaws.com`, once, during server startup. | OBSERVED |
| Any other outbound attempt with telemetry disabled (create/put/scan/TTL cycle plus 5 minutes idle)? | None. No DNS, no TCP, no UDP. | OBSERVED |
| Does it start and serve fully offline with the image already local? | Yes, with `--network none`. One harmless log4j error about the container hostname. | OBSERVED |
| Does AWS say anything about third-party emulators wrapping Local? | Not in the Customer Agreement, Service Terms, IP License, Site Terms, data privacy FAQ, the DynamoDB Local docs, or either registry page. | SOURCE (absence) |
| Is use without an AWS account permitted? | The licence text says it is not. | SOURCE |
| Does that collide with understudy's product claims? | Yes, for "no account". See section 5.2. | INTERPRETATION |

Claims corrected in place in [dynamodb-local.md](dynamodb-local.md): sections 1.1, 1.4, 2, "Implications" items 1 and 11, and the open-questions list (ECR mirror digest confirmed, environment variable and flag verified, "`false` does not work" caveat added).

---

## 1. Licence text across distributions

### 1.1 Artifacts compared

| Channel | Reference | Index digest |
|---|---|---|
| Docker Hub | `amazon/dynamodb-local@sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab` (tags `3.3.1` and `latest`) ([Hub page](https://hub.docker.com/r/amazon/dynamodb-local)) | `sha256:ff89bd48...0dab` |
| ECR Public | `public.ecr.aws/aws-dynamodb-local/aws-dynamodb-local@sha256:ff89bd48...0dab` ([gallery](https://gallery.ecr.aws/aws-dynamodb-local/aws-dynamodb-local)) | identical |
| Zip | [dynamodb_local_latest.zip](https://d1ni2b6xgvw0s0.cloudfront.net/v3.x/dynamodb_local_latest.zip), SHA-256 `5b0d17dd3b4e...`, 50,408,163 bytes. The [v2.x URL](https://d1ni2b6xgvw0s0.cloudfront.net/v2.x/dynamodb_local_latest.zip) returns the same bytes. | n/a |
| Maven | [DynamoDBLocal-3.3.1.jar](https://repo1.maven.org/maven2/software/amazon/dynamodb/DynamoDBLocal/3.3.1/DynamoDBLocal-3.3.1.jar) and its [POM](https://repo1.maven.org/maven2/software/amazon/dynamodb/DynamoDBLocal/3.3.1/DynamoDBLocal-3.3.1.pom); [metadata](https://repo1.maven.org/maven2/software/amazon/dynamodb/DynamoDBLocal/maven-metadata.xml) lists 3.0.0 to 3.3.1 | n/a |

Platform manifests, identical in both registries (OBSERVED with `docker buildx imagetools inspect` on [Hub](https://hub.docker.com/r/amazon/dynamodb-local) and [ECR](https://gallery.ecr.aws/aws-dynamodb-local/aws-dynamodb-local)):

- linux/amd64: `sha256:744bf7138f0e893a6438de89bc3374501105b42bc7473cef6d561f30ff2dec6a`
- linux/arm64: `sha256:0b8779f3e5a761cb41c7b7610d1a67518964a22a9ca063b4a53c8c312b933485`
- attestation manifests: `sha256:b5ed446d94b865f4cd55677c509aa81aa6143cb624b78bbfa85b6596c9810a93` (for arm64) and `sha256:54270720f00f50b4856bf3ea5fdb934f8d3ae00202c03d00de2c4384dceff916` (for amd64)

ECR Public registry `aws-dynamodb-local` has display name "AWS DynamoDB" and `"verified": true` in the gallery API ([getRegistryCatalogData](https://api.us-east-1.gallery.ecr.aws/getRegistryCatalogData), POST `{"registryAliasName":"aws-dynamodb-local"}`). I found no AWS documentation page that names the ECR mirror (UNVERIFIED as a documented channel; OBSERVED as a registry the gallery flags as verified, serving digest-identical content).

### 1.2 File comparison

SHA-256 of each file, extracted from the image at `/home/dynamodblocal/`, from the zip root, and from the Maven jar root.

| File | Hub image | ECR image | Zip (v1.x, v2.x, v3.x) | Maven jar |
|---|---|---|---|---|
| `LICENSE.txt` | `16d139a3...` | identical | identical | identical (also 3.0.0, 3.1.0, 3.2.0, 3.3.0 jars) |
| `README.txt` (8712 bytes, release notes only) | `bf3e339b...` | identical | identical (v3.x zip) | absent |
| `THIRD-PARTY-LICENSES.txt` (27,201 bytes) | `dfb2ea47...` | identical | identical (v3.x zip) | absent |
| Other notice files | none | none | none | `THIRD-PARTY-NOTICES.txt` (ion-java dependency report) and `licenses.md`; POM `<license>` name "DynamoDB Local License Agreement", url `classpath:LICENSE.txt` |

`DynamoDBLocal.jar` has three different file hashes (image `3630bab3...`, zip `27c0a732...`, Maven `41a32e0a...`) but identical length (5,664,607 bytes) and identical content for every member (compared member by member; only archive metadata differs, 14 and 558 bytes). I did not inspect the class files.

Docker Hub description says nothing about licence terms; ECR Public gallery catalog data likewise ([Hub API](https://hub.docker.com/v2/repositories/amazon/dynamodb-local/), [ECR catalog](https://api.us-east-1.gallery.ecr.aws/getRepositoryCatalogData)). Both carry the same text, including "Using DynamoDB local does not require an internet connection". The image has one OCI label (`aws.java.sdk.version=3.3.1`) and no licence label. The image also ships Amazon Corretto 17 with its own legal notices under `/usr/lib/jvm/java-17-amazon-corretto.x86_64/legal/` (OBSERVED).

`THIRD-PARTY-LICENSES.txt` lists the bundled open-source components (annotations, commons-cli, commons-lang, Ion, joda-time, Kotlin, log4j, sqlite4java, Netty, Guava, Jetty, Jackson and others) each under its own licence; it contains no AWS-specific restriction. The `README.txt` mentions telemetry only in two release-note lines (2.1.0 "Adding Telemetry to DynamoDB Local", 2.5.0 "Adding Telemetry to Embedded Mode").

Conclusion: no difference in terms between channels. Choosing ECR Public over Docker Hub changes which registry's terms govern the pull (section 3.5), not the Local licence.

### 1.3 Verbatim clauses

From `LICENSE.txt` (title "Amazon DynamoDB Local License Agreement"), identical in every artifact above. Source: [image](https://hub.docker.com/r/amazon/dynamodb-local) file `/home/dynamodblocal/LICENSE.txt`, also [zip](https://d1ni2b6xgvw0s0.cloudfront.net/v3.x/dynamodb_local_latest.zip) and [jar](https://repo1.maven.org/maven2/software/amazon/dynamodb/DynamoDBLocal/3.3.1/DynamoDBLocal-3.3.1.jar). Typographic quotes are normalised to ASCII; wording is otherwise exact, including the source's spelling.

**Preamble (what binds you and where terms come from)**

> THIS LICENSE AGREEMENT ("LICENSE") GOVERNS YOUR USE OF THE DYNAMODB LOCAL SOFTWARE (TOGETHER WITH ANY UPDATES AND ENHANCEMENTS TO IT, AND ACCOMPANYING DOCUMENTATION, THE "SOFTWARE"). IF YOU INSTALL OR USE THE SOFTWARE, YOU WILL BE BOUND BY THIS LICENSE. UNLESS OTHERWISE DEFINED IN THIS LICENSE, CAPTIALIZED TERMS WILL HAVE THE SAME MEANING AS SET FORTH IN THE AWS CUSTOMER AGREEMENT POSTED AT AWS.AMAZON.COM/AGREEMENT (THE "AWS AGREEMENT").

**Eligibility and permitted use ("License")**

> AWS Licensor or its licensors own all right, title, and interest in and to the Software and all related technology and intellectual property rights. Subject to the terms of this License and the AWS Agreement, AWS Licensor hereby grants you a personal, limited, nonexclusive, non-transferable, non-sublicenseable license to (a) install the Software on computer equipment owned or controlled by you and (b) use the Software solely (i) for your internal business purposes and (ii) in connection with the Services. You may not use the Software if you do not have an AWS account in good standing. Some components of the Software may also be governed by applicable open-source software licenses located in the software component's source code. Your license rights with respect to these individual components are defined by the applicable open-source software license, and nothing in this License will restrict, limit, or otherwise affect any rights or obligations you may have, or conditions to which you may be subject, under such open-source software licenses.

**Redistribution and modification ("Limitations")**

> You may not, and you will not encourage, assist, or authorize any other person to, (a) incorporate any portion of it into your own programs or compile any portion of it in combination with your own programs; or (b) sell, rent, lease, lend, loan, distribute, act as a service bureau, publicly communicate, transform, or sub-license the Software or otherwise assign any rights to the Software in whole or in part; (c) modify, alter, tamper with, repair, or otherwise create derivative works of the Software, or (d) reverse engineer, disassemble, or decompile the Software or apply any other process or procedure to derive the source code of any software included in the Software. All rights granted to you are conditioned on your continued compliance this License, and will immediately and automatically terminate if you do not comply with any term or condition of this License or the AWS Agreement, including any failure to remit timely payment for the Software or the Service.

**Licensor identity ("AWS Licensor")**

> If the geographic location associated with your AWS account is any country within Europe, the Middle East, or Africa ("EMEA"), then Amazon Web Services EMEA SARL ("AWS Europe") grants this License. If the geographic location associated with your AWS account is any country outside of EMEA, then Amazon Web Services, Inc. ("AWS, Inc.") grants this License.

**Updates, termination, amendment**

> In order to keep the Software up to date, AWS Licensor may offer automatic or manual updates at any time. If AWS Licensor elects to provide maintenance or support of any kind, AWS Licensor may terminate that maintenance or support at any time without notice to you.

> You may terminate this License at any time by uninstalling or destroying all copies of the Software that are in your possession or control. Your rights under this License will automatically terminate without notice if you fail to comply with any of its terms or fail to make timely payment. In the case of termination, you must cease all use and destroy all copies of the Software. AWS Licensor may modify, suspend, discontinue, or terminate your right to use part or all of the Software at any time without notice to you, and in that event AWS Licensor may modify the Software to make it inoperable.

> AWS Licensor may amend this License at its sole discretion by posting the revised terms on the AWS website (aws.amazon.com) or within the Software. Your continued use of the Software after any amendment's effective date evidences your agreement to be bound by it.

**Other clauses present (not quoted here)**: Reservation of Rights ("You may not use the Software for any illegal purpose"; structure, organization and code are "trade secrets and confidential information"), Disclaimer of Warranties and Limitation of Liability (aggregate liability capped at "$50.00"), Indemnification (starts "You are liable for and will defend, indemnify, and hold harmless the Released Parties"), Export Regulations, U.S. Government End Users, Conflicts, and a closing Note that terms in `THIRD-PARTY-LICENSES.txt` apply "in lieu of" the software licence for the identified files.

### 1.4 What the text does and does not say

SOURCE facts:

- Eligibility is stated as a prohibition: "You may not use the Software if you do not have an AWS account in good standing."
- The grant is limited to "internal business purposes" and "in connection with the Services".
- "Services" is a capitalised term defined by the AWS Customer Agreement: "'Service' means each of the services made available by us or our affiliates, including those web services described in the Service Terms. Services do not include Third-Party Content." ([Customer Agreement](https://aws.amazon.com/agreement/), last updated August 14, 2026).
- The Software is not itself one of the "Services" in that definition, and the licence does not define the connection that "in connection with" requires.
- The words "emulator", "third-party tool", "proxy", "wrapper" and "CI" do not appear in the licence.
- Redistribution clause (b) covers "distribute"; modification clause (c) covers "modify, alter, tamper with, repair, or otherwise create derivative works".

AWS's own marketing text points the other way: the Hub page says the image lets you "include DynamoDB local in your containerized builds and as part of your continuous integration testing", the DynamoDB Local docs say "Download DynamoDB local for free" and "you don't need an internet connection while you develop your application" ([setup](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.html), [deploying](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.DownloadingAndRunning.html)), and the Hub page invites `docker run -p 8000:8000 amazon/dynamodb-local` with no mention of the account requirement ([Hub page](https://hub.docker.com/r/amazon/dynamodb-local)). The docs also tell users to supply fake credentials (`fakeMyKeyId`). Whether that tension changes the legal position is outside this document.

---

## 2. Network behaviour

### 2.1 Method

- Image: `amazon/dynamodb-local@sha256:ff89bd48...0dab` (3.3.1), started as `java -jar DynamoDBLocal.jar -inMemory -port 8000 [-disableTelemetry]` with `DDB_LOCAL_TELEMETRY=0` set or not, per run ([flags](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.UsageNotes.html); `-help` output in the image lists `-disableTelemetry  When specified, DynamoDB Local will not send any telemetry.`).
- "Sink" runs: a user-defined bridge network created with `--internal` (no route off the host), labelled `understudy.dev/test=1`. A probe container on that network owns the network namespace and runs three things: (1) a route `local 0.0.0.0/0 dev lo` so that a connect to any address lands on a local socket, (2) a recording sink (DNS on UDP and TCP 53 answering every A query with a unique 10.254.x.y address, HTTP on 80, TLS on 443 that logs the ClientHello SNI and then refuses the handshake), (3) `tcpdump -i any` of everything except the Local port 8000. DynamoDB Local joins that namespace (`--network container:`) with a `resolv.conf` pointing at the sink. Nothing can leave the host; every attempt, to any address and port, is either logged by the sink or visible in the capture.
- "None" runs: `--network none` (the probe joined the same way; no route trick, no sink). One further run started Local directly with `--network none` and drove it from a sidecar sharing its namespace (5 s idle only).
- Workload per run, from inside the namespace with curl: wait for any HTTP answer on 8000, then `CreateTable` (PAY_PER_REQUEST, hash key `LockID`), `PutItem`, `Scan`, `UpdateTimeToLive` (`ExpiresAt`), `DescribeTimeToLive`, then 300 s idle. All five calls returned HTTP 200 in every run.
- TLS-terminating variant: the sink issued certificates from a throwaway CA that the JVM trusted through `JAVA_TOOL_OPTIONS` pointing at a PKCS12 truststore built in the test only, and returned canned responses, to see what follows the first hop. Authorization headers were redacted in the log. This changes the JVM's trust configuration, so it shows what Local sends, not what a default image trusts.
- All offsets below are seconds after the DynamoDB container started.

### 2.2 Results

| Run | Network | Opt-out | Idle | Outbound attempts |
|---|---|---|---|---|
| sink-default | internal + sink | none | 300 s | DNS A and AAAA `cognito-identity.us-east-1.amazonaws.com` at +1.73 s; 3 TCP connects to :443 at +1.73, +1.84, +1.86 s, each a TLS ClientHello with that SNI (the sink refused). Nothing afterwards. |
| sink-env | internal + sink | `DDB_LOCAL_TELEMETRY=0` | 300 s | none (0 DNS, 0 SYN) |
| sink-flag | internal + sink | `-disableTelemetry` | 300 s | none |
| sink-both | internal + sink | both | 300 s | none |
| none-default | `--network none` | none | 300 s | none possible; server started and all calls succeeded |
| none-both | `--network none` | both | 300 s | none; all calls succeeded |
| none-direct | `--network none`, Local is the namespace owner | none | 5 s | none possible; all calls succeeded |
| tval-false, tval-FALSE, tval-1, tval-true | internal + sink | `DDB_LOCAL_TELEMETRY=false` / `FALSE` / `1` / `true` | 12 s | same as sink-default in all four (first DNS at +1.46, +1.52, +1.52, +1.43 s; 3 connects each) |
| mitm-default | internal + TLS-terminating sink | none | 300 s | see 2.3 |
| mitm-stop | same | none | 40 s, `docker stop -t 10` at +20 s | same two hosts at startup; nothing at stop |

In the opt-out runs the only captured packets were interface bring-up noise in the first two seconds (IPv6 neighbour and router solicitation, MLD reports, one ARP); no unicast IP packet, no DNS.

Trigger: every attempt happens during server startup, before the first API call (in sink-default the first DNS query at +1.73 s precedes the first successful HTTP answer at +2.28 s). Neither the five API calls nor the idle period produced any further traffic, including in the 300 s TLS-terminating run.

### 2.3 What telemetry sends (TLS-terminating runs, canned responses)

Order and content, from mitm-default (identical sequence in mitm-stop):

1. `POST https://cognito-identity.us-east-1.amazonaws.com/` with `X-Amz-Target: AWSCognitoIdentityService.GetId` and body `{"IdentityPoolId":"us-east-1:a199bca5-0526-4f31-8e8f-2b4a3f4344ca"}` (unsigned, unauthenticated identity pool).
2. Same host, `AWSCognitoIdentityService.GetCredentialsForIdentity`.
3. `POST https://pinpoint.us-east-1.amazonaws.com/v1/apps/5ebafb660937499a859886beacaaca6b/events`, SigV4-signed with the credentials from step 2 (the sink returned fake ones), body:

```json
{"BatchItem":{"<uuid>":{"Endpoint":{"Attributes":{"SETUP_TYPE":["DOCKER"],"JAVA_VERSION":["17.0.19"],"SETUP_MODE":["IN_MEMORY"]},"Demographic":{"AppVersion":"3.3.1","Make":"x86_64","Platform":"LINUX"},"User":{"UserId":"<uuid>"}},"Events":{"<uuid>":{"EventType":"SERVER_STARTUP","Session":{"Id":"<uuid>","StartTimestamp":"<UTC time>"},"Timestamp":"<UTC time>"}}}}}
```

Observations (OBSERVED):

- The user id differed between the two runs, so it is not stable across container starts in a fresh container.
- The User-Agent is `aws-sdk-java/2.49.4 ... os/Linux#6.8.0-142-generic lang/java#17.0.19 ... md/en_US ...`. It carries the host kernel release and locale in addition to the body.
- Region `us-east-1` was used with no `AWS_REGION` set; I did not test whether the region follows the environment (UNVERIFIED).
- The SDK sent `Amz-Sdk-Request: attempt=1; max=3`; with a refused handshake it made three connects, then stopped.
- No event was sent for the five API calls, for the idle period, or on `docker stop` (SIGTERM with 10 s grace). The telemetry page says usage telemetry covers "server start/stop and the API or Operation called" ([page](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocalTelemetry.html)); the stop and per-API events were not observed here. Whether they are batched, sampled, or only emitted on other paths is UNVERIFIED. Real Cognito and Pinpoint responses could change later behaviour; I did not contact them.

### 2.4 Offline start, and one log line

- With the image already local, Local started and served all five calls under `--network none` (none-default, none-both, none-direct). Startup to first HTTP answer was about 1.6 to 2.3 s in these runs (single samples, parallel load, not a benchmark).
- Under `--network none` the container hostname is not in `/etc/hosts`, so log4j prints a stack trace at startup: `main ERROR Could not determine local host name ... java.net.UnknownHostException`. It is harmless (every call succeeded) but would look like a failure in logs. On the internal user-defined network the line does not appear. A `--hostname`/`--add-host` entry would avoid it (not tested).
- The Telemetry page is consistent with the offline result: "If telemetry remains on, DynamoDB local sends telemetry data in the background without requiring any additional customer interaction." ([Telemetry page](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocalTelemetry.html)). A failing telemetry call did not delay or break the API in any run.

### 2.5 Limits of this evidence

- One image version (3.3.1), linux/amd64 only, Docker on Linux. Not tested: arm64, the zip or Maven jar outside Docker, macOS, Windows, `-dbPath`, `-sharedDb`, Streams, other API operations, proxy environment variables (`HTTPS_PROXY` handling is UNVERIFIED), or soak beyond 5 minutes.
- One run per configuration for the 300 s matrix; the telemetry decision is made at startup, so I expect it to be deterministic, but did not repeat.
- Later image versions can change any of this. The egress check should be a permanent test (section 5.3).

---

## 3. AWS terms

All fetched 2026-10-08 with dates as shown on each page.

### 3.1 Customer Agreement ([aws.amazon.com/agreement](https://aws.amazon.com/agreement/), last updated August 14, 2026)

- Defines AWS Content as "APIs, WSDLs, sample code, software libraries, command line tools, proofs of concept, templates, advice, information, programs (including credit programs) and any other Content made available by us and our affiliates related to use of the Services or on the AWS Site and other related technology ... AWS Content does not include the Services or Third-Party Content."
- 6.3: "The Intellectual Property License applies to your use of AWS Content and the Services." 6.4 forbids using AWS Content or Services "in any manner or for any purpose other than as expressly permitted by this Agreement", reverse engineering, use "in a way intended to avoid incurring fees or exceeding usage limits or quotas", and reselling.
- The words "DynamoDB", "emulat" and "local software" do not occur (searched the full page text).

### 3.2 Service Terms ([aws.amazon.com/service-terms](https://aws.amazon.com/service-terms/), last updated October 1, 2026)

- The only DynamoDB section is 19, "Amazon DynamoDB", which covers Reserved Capacity. "DynamoDB Local", "emulat" and "offline" do not occur.
- 1.1: "You may not transfer outside the Services any software (including related documentation) you obtain from us or third party licensors in connection with the Services without specific authorization to do so."
- 1.4: "In connection with your use of the Services, you are responsible for maintaining licenses and adhering to the license terms of any software you run."
- Section 80 (Amazon ECR Public) is in 3.5.

### 3.3 Intellectual Property License ([aws.amazon.com/legal/aws-ip-license-terms](https://aws.amazon.com/legal/aws-ip-license-terms/), last updated October 27, 2025)

- Section 3 grants "a limited, royalty-free, revocable, non-exclusive, non-sublicensable, non-transferrable license to copy and use the AWS Content solely in connection with your permitted use of the Services during the Term"; section 4 forbids "(a) modify, distribute, alter, tamper with, repair, or otherwise create derivative works of any Content included in the Services or AWS Content ... or (b) sublicense the Services or AWS Content".
- The Local licence describes itself as a "separate agreement" that is "Subject to ... the AWS Agreement". How the IP License and the Local licence interact is not stated anywhere I found (INTERPRETATION needed).

### 3.4 Site Terms ([aws.amazon.com/terms](https://aws.amazon.com/terms/), last updated June 4, 2025)

- Covers the AWS Site: a "limited license to access and make personal use of the AWS Site and not to download (other than page caching) or modify it" and no "commercial use". Nothing about DynamoDB Local or emulators. The Local downloads are offered through the docs, but the Site Terms do not mention them (how the two interact is for a lawyer).

### 3.5 Registry terms

- ECR Public, Service Terms 80.1 to 80.3 ([service-terms](https://aws.amazon.com/service-terms/)): ECR Public "allows you to upload and share Content that anyone with or without an AWS account ('Registry Users') can download and use". 80.3: "You may specify the terms under which you license Your Content to Registry Users." So pulling from ECR Public needs no account; the content's terms are the publisher's, here the image's `LICENSE.txt`.
- Docker Hub, Terms of Use effective August 26, 2026 ([docker.com/legal/docker-terms-service](https://www.docker.com/legal/docker-terms-service/)): anonymous pulls fall under "Free Hub Access", "subject to the rate limits and usage restrictions posted at https://www.docker.com/pricing/ and https://docs.docker.com/docker-hub/download-rate-limit/", and Docker "reserves the right to modify, restrict, or discontinue Free Hub Access at any time". "All third party images shall be deemed Third-Party Content subject to their corresponding terms and conditions."
- The telemetry page links to "AWS service terms" and the "Data privacy FAQ" ([FAQ](https://aws.amazon.com/compliance/data-privacy-faq/)) for the data it collects; neither page contains the word "telemetry" or "DynamoDB Local" (searched).

### 3.6 Third-party emulators

No statement about third-party emulators, wrapping, proxying, CI use or redistribution of DynamoDB Local was found in any document in 3.1 to 3.5, the Local licence, the DynamoDB Local docs ([setup](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.html), [usage notes](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.UsageNotes.html)), or the two registry pages. Absence in these pages is the only claim; I did not contact AWS.

---

## 4. How others handle it

| Project | Mechanism | Licence notice found | Telemetry opt-out |
|---|---|---|---|
| LocalStack (archived, `pushed_at` 2026-03-23; [repo](https://github.com/localstack/localstack)) | Downloads `v2.x/dynamodb_local_latest.zip` from CloudFront at first use ([packages.py](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/dynamodb/packages.py)); that URL serves the same bytes as `v3.x` (section 1.1). Rewrites the jar: `zip -u DynamoDBLocal.jar log4j2.xml` and adds a `ddb-local-loader` javaagent plus Javassist, both downloaded. | None found on the [DynamoDB docs page](https://docs.localstack.cloud/aws/services/dynamodb/), which says only "DynamoDB emulation is powered by DynamoDB Local". Repository code search for a licence notice returned nothing (the search tool is weak; UNVERIFIED). | Sets only `DDB_LOCAL_TELEMETRY=0`, never the flag ([server.py](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/dynamodb/server.py)). Works, per section 2. |
| Floci (MIT; [floci-io/floci](https://github.com/floci-io/floci), active) | Default engine is its own `native` DynamoDB. Optional `local` backend forwards to a DynamoDB Local the user runs: "Floci does not start DynamoDB Local. Run it yourself, for example as a container" ([DynamoDB docs](https://github.com/floci-io/floci/blob/main/docs/services/dynamodb.md)); its compose example uses `amazon/dynamodb-local:3.3.1`. Rejects `amazonaws.com` endpoints ([DynamoDbLocalBackend.java](https://github.com/floci-io/floci/blob/main/src/main/java/io/github/hectorvent/floci/services/dynamodb/backend/DynamoDbLocalBackend.java)). | None in that doc (no mention of the licence or an AWS account). Its README says "No account. No auth token." ([README](https://github.com/floci-io/floci)) which holds for the native default. | Not set by Floci (the user runs Local), so the default image telemetry applies unless the user opts out. |
| serverless-dynamodb-local (MIT; [99x](https://github.com/99x/serverless-dynamodb-local), last push 2024-07-31) | Explicit user step `sls dynamodb install` downloads the Java program; "If using docker, this step is not required." | README states only the plugin's MIT licence ([README](https://github.com/99x/serverless-dynamodb-local#readme)). | Not examined. |
| AWS itself | Hub page and docs advertise a free download, Docker use in CI, and no internet need. | No account statement on the Hub or ECR page; the requirement exists only in `LICENSE.txt`. | Documented, off by flag or `DDB_LOCAL_TELEMETRY=0`. |

Pattern: nobody bundles; nobody found prints the licence on first pull; the closest to a safeguard is Floci's choice to leave installing Local to the user.

---

## 5. Recommendations

### 5.1 First-pull notice (proposed text)

Shown once per digest before understudy pulls Local, and again whenever the pinned digest changes. Non-interactive runs (CI) need an explicit acceptance flag or environment variable; without it understudy leaves the DynamoDB service disabled with a clear error and keeps every other service running. Names `--accept-dynamodb-local-license` and `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE` are placeholders for the architecture author.

```
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

Accept and pull? [y/N]    (non-interactive: --accept-dynamodb-local-license)
```

Product decisions for the notice, not facts: gate versus print-only; where acceptance is stored; wording reviewed by counsel. The licence binds "if you install or use", and understudy performs the pull on the user's behalf, so a gate lets the user decide before that happens (INTERPRETATION).

### 5.2 Product statements that need an exception or a caveat

Quotes are understudy's phase 1 product claims.

| Product statement | Problem | Suggested handling |
|---|---|---|
| "no account" | The DynamoDB path requires, per the Local licence, "an AWS account in good standing" (SOURCE). All other services have no such requirement. | Exception, stated plainly: "No account, except that the DynamoDB service runs on AWS's DynamoDB Local, whose licence requires one." |
| "no token" | True for understudy. Docker Hub pulls are anonymous but rate-limited (section 3.5); ECR Public needs no account. | Keep. Mention the ECR Public fallback. |
| "no telemetry" | True for understudy only if it passes the opt-out. Default Local sends startup telemetry to two `amazonaws.com` hosts. `false` does not disable it; only `0` or the flag does (OBSERVED). | Keep with a requirement: always pass both `-disableTelemetry` and `DDB_LOCAL_TELEMETRY=0`, and test egress (5.3). |
| "no network access needed at runtime" | True once images are local (OBSERVED, `--network none`). The claim already carves out first pulls. | Keep; the carve-out must also say the licence notice happens at that moment. |
| "Instant, tiny, offline" | Same as above. | Keep, same caveat. |
| "The one exception is pulling Docker images for Lambda and DynamoDB Local on first use." | Incomplete: the DynamoDB Local pull also brings a licence with account terms. The Lambda base images carry their own terms, not examined here (UNVERIFIED). | Add the licence consequence for DynamoDB Local; open a separate check for the Lambda images. |
| Services table, DynamoDB: "AWS's own emulator, pulled at runtime and never bundled" | Accurate. "understudy-managed" automation is the part with no precedent in section 4. | Keep; raise it in the legal review (5.4). |
| "License: Apache-2.0" and any README "fully open source" wording | A runtime dependency is proprietary. | README: "understudy is Apache-2.0. The DynamoDB service uses AWS's DynamoDB Local, a proprietary component with its own licence, pulled at runtime." |
| Any README or site copy implying anyone can run every phase 1 service with no account | Same as the account row. | Do not describe the DynamoDB service as account-free. |

### 5.3 Engineering consequences

- Pass both opt-outs and treat only the literal `0` as valid for the environment variable (OBSERVED). [dynamodb-local.md](dynamodb-local.md) already recommends passing both; this adds the reason.
- Add a permanent egress test: run the pinned Local with understudy's exact arguments on a `--internal` network with the recording sink and assert zero DNS and zero connections after startup and over a cycle. It catches an image bump that changes telemetry behaviour.
- Consider running the Local container on a Docker network with no external route (OBSERVED working for Local on an `--internal` network; whether the host-side proxy can still reach it, and how that interacts with published ports, was not tested).
- Pin by digest (the ECR Public digest is identical, so either registry satisfies the pin). Do not re-tag, `docker save`, or push a derived image.
- If the container runs with `--network none` or similar, supply a hostname entry to avoid the log4j stack trace (not tested).

### 5.4 Legal questions to put to counsel (INTERPRETATION required)

1. Does an understudy user with no AWS account violate "You may not use the Software if you do not have an AWS account in good standing", and does anything fall on understudy for arranging the pull?
2. Does using Local as a backend for another emulator, with no AWS use in the workflow, count as use "in connection with the Services"?
3. Does automated install by understudy on the user's machine satisfy "install the Software on computer equipment owned or controlled by you"? CI runners owned by a third party are a separate case.
4. Do the Docker Hub and ECR Public pages, and the AWS docs that say "Download DynamoDB local for free" and "you don't need an internet connection", affect any of the above?
5. Is routing through a proxy that rewrites ARNs, partitions accounts and injects faults "incorporat[ing] any portion of it into your own programs"? (The proxy talks to Local only over HTTP and never touches its files.)
6. Is printing the notice enough, or is an explicit acceptance step needed?

---

## 6. Open items (UNVERIFIED)

- Whether telemetry follows `AWS_REGION` or other environment (only the default was run).
- Whether per-API-call and shutdown events exist and under what conditions (none observed).
- Behaviour of telemetry on arm64, macOS, Windows, and the non-Docker distributions.
- `HTTP(S)_PROXY` handling by the telemetry client.
- Whether AWS documents the ECR Public mirror as an official channel.
- Terms of the Lambda base images pulled from `public.ecr.aws/lambda/*`. Closed 2026-10-14: no restriction found; pull and run them freely, never re-host them.

## 7. Reproducing the egress test

No scripts are checked in (the harness lived in a scratch directory). To rebuild: (1) `docker network create --internal --label understudy.dev/test=1 <net>`; (2) start a probe container with `--cap-add NET_ADMIN --cap-add NET_RAW` that runs `ip route add local 0.0.0.0/0 dev lo table local`, a DNS (UDP and TCP 53) plus TCP 80 and 443 sink that logs every query and SNI, and `tcpdump -i any -nn -tt 'not (tcp port 8000)'`; (3) bind-mount a `resolv.conf` with a placeholder nameserver into both containers; (4) start Local with `--network container:<probe>` and drive it with curl from the probe over `127.0.0.1:8000` using the `X-Amz-Target: DynamoDB_20120810.<Operation>` header and any `Authorization` credential string. A conformance suite version of this belongs outside the main module, with its own dependencies (the main module is standard library only).
