# DynamoDB Local as an embedded backend

Research for the phase 1 DynamoDB service: understudy proxies DynamoDB data-plane calls to an understudy-managed DynamoDB Local container and shims what Local lacks. Date of research: 2026-10-08. Version under test: `amazon/dynamodb-local:3.3.1`, digest `sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab`.

## Method and evidence labels

- **Observed** means: I ran the image locally (`docker run`, labeled `understudy.dev/test=1`, ephemeral host ports, fake credentials, removed afterwards) and called it with curl, the AWS CLI 2.x and Terraform 1.16.0 + AWS provider 6.68.0. No real AWS endpoint was called. Observed claims cite the image page: [amazon/dynamodb-local](https://hub.docker.com/r/amazon/dynamodb-local).
- **Source** claims cite the document or source file that owns the fact.
- Anything I could not confirm is marked **UNVERIFIED**.
- I did not decompile or disassemble the Local jar (the license forbids it, see section 2). Everything about Local internals is black-box behavior or published documentation.

---

## 1. Image, versions, flags

### 1.1 Distribution channels

- Three channels: JAR download (needs a JRE), Maven dependency, Docker image ([DynamoDB local setup](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.html)).
- Docker Hub: `amazon/dynamodb-local`, Verified Publisher "Amazon Web Services", 486 stars, ~460M pulls, last updated 2026-07-31 ([Hub API](https://hub.docker.com/v2/repositories/amazon/dynamodb-local/)).
- Mirror: `public.ecr.aws/aws-dynamodb-local/aws-dynamodb-local` has the same tag list, and the `3.3.1` manifest digest equals Docker Hub's (`sha256:ff89bd48...0bab`) (observed via the [ECR Public registry API](https://public.ecr.aws/v2/aws-dynamodb-local/aws-dynamodb-local/tags/list); the mirror is not mentioned in the docs I read, so treat it as unofficial-but-AWS-published UNVERIFIED). Correction (G5, 2026-10-08): the full index, both platform manifests and both attestation manifests are identical in the two registries ([Hub](https://hub.docker.com/r/amazon/dynamodb-local), [ECR gallery](https://gallery.ecr.aws/aws-dynamodb-local/aws-dynamodb-local)), and the gallery API flags the `aws-dynamodb-local` registry as `"verified": true`, display name "AWS DynamoDB" ([getRegistryCatalogData](https://api.us-east-1.gallery.ecr.aws/getRegistryCatalogData)). No AWS doc page naming the mirror was found, so "documented channel" stays UNVERIFIED. Details in [dynamodb-local-terms.md](dynamodb-local-terms.md) section 1.1.
- Docker Hub tags are mutable (`immutable_tags_settings.enabled=false` in the [Hub API](https://hub.docker.com/v2/repositories/amazon/dynamodb-local/)). Pin by digest.

### 1.2 Version lines and current tags

- Docs: "v3.x (Current), v2.x (Legacy), and v1.x (Deprecated)"; v3.x recommended ([Deploying DynamoDB locally](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.DownloadingAndRunning.html)).
- Maven Central has `software.amazon.dynamodb:DynamoDBLocal` 3.0.0, 3.1.0, 3.2.0, 3.3.0, 3.3.1; latest/release = 3.3.1, lastUpdated 2026-07-31 ([maven-metadata.xml](https://repo1.maven.org/maven2/software/amazon/dynamodb/DynamoDBLocal/maven-metadata.xml)).
- Docker Hub tags, newest first (`last_updated`, from the [tags API](https://hub.docker.com/v2/repositories/amazon/dynamodb-local/tags?page_size=15&ordering=last_updated)): `latest` and `3.3.1` (2026-07-31), `3.3.0` (2026-01-19), `3.2.0` (2026-01-12), `3.1.0` (2025-09-12), `3.0.0` (2025-07-17), `2.6.1`, `2.6.0`, `2.5.x`..., `1.25.1` (2024-11-06). The ECR mirror also lists 1.11.119 through 1.25.1.
- Sizes (Hub manifest sizes): amd64 238,997,978 B, arm64 237,412,728 B for 3.3.1. Both architectures exist, so a multi-arch pull works on Apple Silicon and Linux ARM ([tags API](https://hub.docker.com/v2/repositories/amazon/dynamodb-local/tags?page_size=15&ordering=last_updated)). `docker history` layer sizes sum to ~518 MB unpacked (167 MB Amazon Linux base, 291 MB yum layer with `java-17-amazon-corretto-devel` and `sqlite`, 60 MB app tarball) (observed).
- Release notes shipped inside the image (`/home/dynamodblocal/README.txt`, observed):
  - 3.3.1 (2026-05-28): PartiQL deps in the pom, SDK 2.48.3.
  - 3.3.0 (2026-01-16): multi-key support for GSIs.
  - 3.2.0 (2026-01-07): `ShardFilter` on `DescribeStream`, DeletionProtection fix on `UpdateTable`.
  - 3.0.0 (2025-06-26): migrated from AWS SDK Java v1 to v2.
  - 2.6.0 (2025-02-25): "Added support for using Table ARN as table name in several DynamoDB APIs".
  - 2.5.0 (2024-05-28): OnDemand throughput, telemetry in embedded mode.
  - 2.2.0 (2023-12-14): table delete protection.
  - 1.11.119 (2017-04-13): "Add TTL implementation".
  - 1.11.475 (2019-02-04): on-demand, 20 GSIs, transactions.
  - 1.15.0 (2021-02-08): PartiQL.
  - 1.13.0 (2020-05-20): 25 items / 4 MB per transaction.
- Runtime: the image is `FROM` Amazon Linux with Corretto 17; `ENTRYPOINT ["java"]`, `CMD ["-jar","DynamoDBLocal.jar","-inMemory"]`, `USER dynamodblocal` (uid 1000), `WORKDIR /home/dynamodblocal`, `EXPOSE 8000`, label `aws.java.sdk.version=3.3.1` (observed via `docker image inspect` and `docker history`). JRE 17+ is required from 2.6.0 ([setup](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.DownloadingAndRunning.html)). Jetty 12.1.11 serves HTTP (`Server: Jetty(12.1.11)` response header, observed).

### 1.3 Startup flags

From `-help` output of 3.3.1 (observed) and the [usage notes](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.UsageNotes.html):

| Flag | Meaning |
|---|---|
| `-port <n>` | default 8000 |
| `-inMemory` | no database files; data lost on stop; cannot combine with `-dbPath` |
| `-dbPath <dir>` | directory for SQLite files; default is cwd |
| `-sharedDb` | one database for all credentials and regions |
| `-delayTransientStatuses` | simulates CREATING/DELETING for GSIs only |
| `-optimizeDbBeforeStartup` | requires `-dbPath` |
| `-cors <allow-list>` | default `*` |
| `-disableTelemetry` | see 1.4 |
| `-version` | prints `3.3.1` |
| `-help` | usage |

Startup banner printed by Local (observed):

```
Initializing DynamoDB Local with the following configuration:
Port:	8000
InMemory:	true
Version:	3.3.1
DbPath:	null
SharedDb:	false
shouldDelayTransientStatuses:	false
CorsParams:	null
```

Resource footprint (observed): first successful `ListTables` 1.25 s after `docker run -d` with the image already local; idle memory ~152-156 MiB; `GET /` without an `Authorization` header returns HTTP 400 `MissingAuthenticationToken` (a usable liveness probe: any HTTP response means the server is up).

Container user is uid 1000. A bind-mounted `-dbPath` directory must be writable by uid 1000 (observed: a `chmod 777` host dir worked; the docs' compose example mounts `./docker/dynamodb` at `/home/dynamodblocal/data` with `working_dir: /home/dynamodblocal` and `-dbPath ./data` ([setup](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.DownloadingAndRunning.html))).

### 1.4 Telemetry

- Local sends telemetry unless disabled: "generic usage metrics, systems and environment information, and errors"; usage = "server start/stop and the API or Operation called"; environment = Java version, OS, how Local runs, "hash values of usage attributes" ([Telemetry in DynamoDB local](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocalTelemetry.html)).
- Disable with `-disableTelemetry` or env `DDB_LOCAL_TELEMETRY=0` (same page). LocalStack sets both the env var and runs without the flag ([server.py](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/dynamodb/server.py)). understudy's brief promises "no telemetry": pass both.
- Correction (G5, 2026-10-08), replacing the earlier "UNVERIFIED" notes: on `amazon/dynamodb-local@sha256:ff89bd48...0dab` (3.3.1), `DDB_LOCAL_TELEMETRY=0` alone and `-disableTelemetry` alone each produced zero DNS queries and zero connections over a CreateTable/PutItem/Scan/UpdateTimeToLive cycle plus 5 minutes idle on an internal network with a recording sink. The values `false`, `FALSE`, `1` and `true` did not disable it, although the [Telemetry page](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocalTelemetry.html) says to set the variable to `false`; only the literal `0` works. With telemetry on, Local contacts `cognito-identity.us-east-1.amazonaws.com` and `pinpoint.us-east-1.amazonaws.com` once during startup and sends nothing at `docker stop`. The server starts and answers fully under `--network none`; telemetry cannot be sent there and nothing breaks. Evidence, payload and limits: [dynamodb-local-terms.md](dynamodb-local-terms.md) section 2.

---

## 2. License and redistribution

The license file is `/home/dynamodblocal/LICENSE.txt` inside the image ("Amazon DynamoDB Local License Agreement") (observed from the [image](https://hub.docker.com/r/amazon/dynamodb-local)). The Maven POM declares `<license><name>DynamoDB Local License Agreement</name><url>classpath:LICENSE.txt</url>` ([DynamoDBLocal-3.3.1.pom](https://repo1.maven.org/maven2/software/amazon/dynamodb/DynamoDBLocal/3.3.1/DynamoDBLocal-3.3.1.pom)). It is a proprietary agreement, not an open-source license. The Docker Hub page states no license ([Hub page](https://hub.docker.com/r/amazon/dynamodb-local)).

Operative clauses (verbatim from `LICENSE.txt` in the 3.3.1 image):

- Grant: "a personal, limited, nonexclusive, non-transferable, non-sublicenseable license to (a) install the Software on computer equipment owned or controlled by you and (b) use the Software solely (i) for your internal business purposes and (ii) in connection with the Services. You may not use the Software if you do not have an AWS account in good standing."
- Limitations: "You may not, and you will not encourage, assist, or authorize any other person to, (a) incorporate any portion of it into your own programs or compile any portion of it in combination with your own programs; or (b) sell, rent, lease, lend, loan, distribute, act as a service bureau, publicly communicate, transform, or sub-license the Software or otherwise assign any rights to the Software in whole or in part; (c) modify, alter, tamper with, repair, or otherwise create derivative works of the Software, or (d) reverse engineer, disassemble, or decompile the Software..."
- Unilateral change and kill: "AWS Licensor may modify, suspend, discontinue, or terminate your right to use part or all of the Software at any time without notice to you, and in that event AWS Licensor may modify the Software to make it inoperable." Also "AWS Licensor may amend this License at its sole discretion".
- Bundled third-party components (Jetty, Jackson, Netty, sqlite4java, Guava, Kotlin, log4j, ...) carry their own licenses listed in `THIRD-PARTY-LICENSES.txt`; "nothing in this License will restrict... rights... under such open-source software licenses".

Consequences for an Apache-2.0 open-source project (engineering reading, not legal advice; get a lawyer's review before release):

- **Bundling is not allowed.** Shipping the JAR or image inside understudy's binary, release archives or Docker image is "distribute" under (b); a derived image pushed to any registry is also distribution plus "derivative work" under (c). The brief's rule "pulled at runtime and never bundled" is the safe reading. Do not `docker save`/re-tag/mirror the image.
- **Pulling at runtime on the end user's machine** is the established pattern: LocalStack downloads `https://d1ni2b6xgvw0s0.cloudfront.net/v2.x/dynamodb_local_latest.zip` at first use instead of shipping it ([packages.py](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/dynamodb/packages.py)). Each user installs it on their own machine, so the user, not understudy, is the licensee.
- **The user must hold an AWS account in good standing and use it "in connection with the Services".** An understudy user with no AWS account is technically outside the grant. Surface this in docs and on first pull (print the license notice and a link). Whether using Local purely as a backend for another emulator counts as "in connection with the Services" is UNVERIFIED and a legal question. (G5, 2026-10-08: the licence text is byte-identical on Docker Hub, ECR Public, the zip and the Maven jar; no AWS document addresses third-party emulators; proposed notice text and the list of brief statements needing a caveat are in [dynamodb-local-terms.md](dynamodb-local-terms.md) sections 1, 3 and 5.)
- **Do not modify or patch Local.** LocalStack applies a Javassist agent to the jar (`ddb-local-loader` javaagent, [packages.py](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/dynamodb/packages.py)); that reads as clause (c). understudy should treat Local as a sealed black box reached over HTTP. Talking to it over HTTP is not "incorporat[ing] any portion of it into your own programs or compil[ing]... in combination" on its face (UNVERIFIED legally).
- Because AWS can change terms or render the software inoperable at will, keep the DynamoDB service degradable: a clear error naming Local as the cause, and a pinned-by-digest default with an override.

---

## 3. Data partitioning by access key and region

Documented ([usage notes](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.UsageNotes.html)):

- With `-sharedDb`: a single file `shared-local-instance.db`; every client sees it.
- Without `-sharedDb`: file named `myaccesskeyid_region.db` from "the AWS access key ID and AWS Region as they appear in your application configuration".
- With `-inMemory`: no files, the same partitioning applies in memory (observed below).
- "DynamoDB local `AWS_ACCESS_KEY_ID` can contain only letters (A-Z, a-z) and numbers (0-9)."
- "AWS Regions and distinct AWS accounts are not supported at the client level."

Observed on 3.3.1:

- Key and region are read from the SigV4 `Authorization` header `Credential=<accessKey>/<date>/<region>/dynamodb/aws4_request`. The signature is not validated (a literal `Signature=deadbeef` works).
- `-inMemory`, no `-sharedDb`: a table created under `AKIAEXAMPLE111`/`us-east-1` is invisible to `AKIAEXAMPLE111`/`eu-west-1` and to `AKIAOTHER222`/`us-east-1` (`{"TableNames":[]}`). So in-memory mode partitions per (key, region) too.
- `-sharedDb -inMemory`: a table created by `111111111111`/`us-east-1` is listed for `222222222222`/`eu-west-1`.
- `-dbPath ./data`, no `-sharedDb`: files created lazily, named exactly `<accessKey>_<region>.db`: `111111111111_us-east-1.db`, `111111111111_eu-west-1.db`, `222222222222_eu-west-1.db`. Even a bare `ListTables` for a new pair creates a file.
- A 12-digit all-numeric access key works (so an AWS account id can be used directly as the Local access key).
- Any region string is accepted: `Not A Region!` and an empty string both return `{"TableNames":[]}`. Whether those create odd file names (path characters in a region) is UNVERIFIED; since `-dbPath` embeds the region in a filename, the proxy must only forward region values from its own validated set. The 1.20.0 release note "Fixed path traversal issue" ([README.txt in image](https://hub.docker.com/r/amazon/dynamodb-local)) suggests Local sanitizes, but do not rely on it.
- Persistence round trip: with `-dbPath` on a bind mount, `docker stop` then `docker start` kept tables, items, the TTL specification and the stream definition (observed). The host port changes on restart if published with `-p 127.0.0.1:0:8000`; re-read it with `docker port`.
- Key that violates the character rule: HTTP 400 `{"__type":"com.amazonaws.dynamodb.v20120810#UnrecognizedClientException","Message":"The Access Key ID or security token is invalid."}` (observed with `AKIA_bad-key`).
- No `Authorization` header: HTTP 400 `{"__type":"com.amazonaws.dynamodb.v20120810#MissingAuthenticationToken","Message":"Request must contain either a valid (registered) AWS access key ID or X.509 certificate."}` (observed).

Prior art: LocalStack forwards one request stream to Local and rewrites the credential so Local does the namespacing: `get_ddb_access_key` returns `f"{account_id}{region_name}".replace("-", "")` and `modify_context_region` rewrites `Credential=` in the `Authorization` header, with the comment "DDBLocal supports namespacing as an undocumented feature" ([utils.py](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/dynamodb/utils.py), [provider.py](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/dynamodb/provider.py)). I reproduced the mechanism independently.

Account and region in everything Local returns are fixed, regardless of the credential scope (observed): `TableArn` is always `arn:aws:dynamodb:ddblocal:000000000000:table/<name>`, GSI `IndexArn` is `arn:aws:dynamodb:ddblocal:000000000000:table/<t>/index/<i>`, `LatestStreamArn` is `arn:aws:dynamodb:ddblocal:000000000000:table/<t>/stream/<label>`, stream records carry `"awsRegion":"ddblocal"`. LocalStack comments the same ("DynamoDBLocal hardcodes 'ddblocal' as the region") and regex-rewrites responses ([utils.py `modify_ddblocal_arns`](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/dynamodb/utils.py)).

---

## 4. Behavior differences from DynamoDB

### 4.1 Documented ([usage notes](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.UsageNotes.html))

- Provisioned throughput is ignored; `Scan` is sequential (`Segment`/`TotalSegments` ignored); table operations are instant and state is always ACTIVE (only GSI create/delete pass through CREATING/DELETING); reads are eventually consistent but "most reads appear to be strongly consistent".
- Item collection metrics not tracked; the 1 MB result limit counts the whole item for index queries.
- Streams shard creation differs ("no table partitioning").
- "`TransactionConflictExceptions` aren't thrown by downloadable DynamoDB for transactional APIs."
- Table names are case insensitive in Local (case sensitive on AWS).
- "Tagging is not supported in the downloadable version of DynamoDB."
- "DynamoDB local doesn't support Point-in-time recovery (PITR)."
- `ExecuteStatement` ignores `Limit`. `billingModeSummary` is "always null" per the docs (see 4.2: not true for PAY_PER_REQUEST in 3.3.1; documentation and behavior disagree, prefer behavior).
- Vector indexes: `CreateTable` accepts `VectorIndexes` and ignores it.

### 4.2 Observed on 3.3.1 (all verified by calling the container)

Control plane:

| Call | Local result |
|---|---|
| `CreateTable` valid | 200, `TableStatus":"ACTIVE"` immediately |
| `CreateTable` table/index name < 3 chars | 400 `com.amazon.coral.validate#ValidationException` "Invalid table/index name.  Table/index names must be between 3 and 255 characters long, and may contain only the characters a-z, A-Z, 0-9, '_', '-', and '.'" (applies to index names too) |
| `CreateTable` with `Tags` | 200, tags silently dropped |
| `CreateTable` with `SSESpecification`, `TableClass` | 200, accepted, ignored; response and `DescribeTable` carry no `SSEDescription` and no `TableClassSummary` |
| `DescribeTable` | no `WarmThroughput`, no `TableId`, no `SSEDescription`, no `TableClassSummary`; `BillingModeSummary` only for PAY_PER_REQUEST (absent for PROVISIONED) |
| `DescribeTable` with `TableName` = a table ARN | works; the account and region inside the ARN are ignored (`arn:aws:dynamodb:us-east-1:123456789012:table/Locks` resolved the table) |
| `DeleteTable` | 200, returned `TableDescription.TableStatus` is `ACTIVE` (AWS returns DELETING); a later `DescribeTable` returns ResourceNotFound |
| `DeleteTable`/`DescribeTable`/`DescribeTimeToLive` on missing table | 400 `com.amazonaws.dynamodb.v20120810#ResourceNotFoundException`, message "Cannot do operations on a non-existent table" (AWS message is "Requested resource not found") |
| `UpdateTable` with only `SSESpecification`, `TableClass` or `WarmThroughput` | 400 `ValidationException` "Nothing to update" |
| `UpdateTable` with `DeletionProtectionEnabled`, `StreamSpecification`, `BillingMode`+`ProvisionedThroughput` | works |
| `DeleteTable` while deletion protection is on | 400 `ValidationException` "Resource cannot be deleted as it is currently protected against deletion. Disable deletion protection first." |
| `DescribeLimits` | 200: `AccountMaxReadCapacityUnits 80000`, `AccountMaxWriteCapacityUnits 80000`, `TableMaxReadCapacityUnits 40000`, `TableMaxWriteCapacityUnits 40000` |
| `DescribeContinuousBackups`, `UpdateContinuousBackups` | 400 `UnknownOperationException` "An unknown operation was requested." |
| `ListTagsOfResource`, `TagResource` (and by extension `UntagResource`) | 400 `com.amazonaws.dynamodb.v20120810#UnknownOperationException`, message exactly "Tagging is not currently supported in DynamoDB Local." |
| `DescribeEndpoints`, `DescribeKinesisStreamingDestination`, `DescribeContributorInsights`, `GetResourcePolicy`, `DescribeTableReplicaAutoScaling`, any bogus target | 400 `UnknownOperationException` "An unknown operation was requested." |

Sample `CreateTable` response for PAY_PER_REQUEST (observed; note ARN region `ddblocal`, zero throughput, no WarmThroughput):

```json
{"TableDescription":{"AttributeDefinitions":[{"AttributeName":"LockID","AttributeType":"S"}],"TableName":"Locks","KeySchema":[{"AttributeName":"LockID","KeyType":"HASH"}],"TableStatus":"ACTIVE","CreationDateTime":1791434323.298,"ProvisionedThroughput":{"LastIncreaseDateTime":0.000,"LastDecreaseDateTime":0.000,"NumberOfDecreasesToday":0,"ReadCapacityUnits":0,"WriteCapacityUnits":0},"TableSizeBytes":0,"ItemCount":0,"TableArn":"arn:aws:dynamodb:ddblocal:000000000000:table/Locks","BillingModeSummary":{"BillingMode":"PAY_PER_REQUEST","LastUpdateToPayPerRequestDateTime":1791434323.298},"DeletionProtectionEnabled":false}}
```

Data plane (all as expected on the lock-table workload from the brief):

- `PutItem` with `ConditionExpression: "attribute_not_exists(LockID) OR ExpiresAt < :now"` succeeds on a free key and returns 400 `com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException`, message "The conditional request failed" when held and unexpired (observed). With `ReturnValuesOnConditionCheckFailure: ALL_OLD` the body gains `"Item":{...}` (observed).
- `GetItem` without the key: 400 `com.amazon.coral.validate#ValidationException` "Cannot have null key for GetItem, DeleteItem, or UpdateItem".
- `TransactWriteItems` is implemented. A failed condition returns 400 `com.amazonaws.dynamodb.v20120810#TransactionCanceledException` with `CancellationReasons:[{"Code":"None"},{"Code":"ConditionalCheckFailed","Message":"The conditional request failed."}]` and message "Transaction cancelled, please refer cancellation reasons for specific reasons [None, ConditionalCheckFailed]" (observed).
- PartiQL `ExecuteStatement` (`SELECT * FROM "Locks" WHERE LockID = ?`) works (observed).
- `ItemCount` and `TableSizeBytes` in `DescribeTable` update immediately after writes (observed: ItemCount 2 right after two puts); AWS updates them about every six hours (AWS behavior, UNVERIFIED here).
- Error `__type` values: data-plane errors use `com.amazonaws.dynamodb.v20120810#<Name>`; request-validation errors use `com.amazon.coral.validate#ValidationException` (observed, same as the real service).
- The Smithy model defines the service as `aws.protocols#awsJson1_0`, SigV4, `arnNamespace: dynamodb`, plus `clientEndpointDiscovery` through `DescribeEndpoints` ([dynamodb-2012-08-10.json](https://github.com/aws/api-models-aws/blob/main/models/dynamodb/service/2012-08-10/dynamodb-2012-08-10.json)). The Local-unsupported `DescribeEndpoints` only matters for clients that opt into endpoint discovery; with a custom endpoint override SDKs do not use it (UNVERIFIED per SDK).

---

## 5. TTL

### 5.1 AWS semantics to emulate

- The TTL attribute must be a Number holding Unix epoch seconds; non-Number values are ignored by TTL ([How TTL works](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/howitworks-ttl.html)).
- "Items that have expired and not been deleted will still show up in reads, queries, and scans." Deletion is best effort, "typically within two days"; the docs also say "within a few days" ([UpdateTimeToLive](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_UpdateTimeToLive.html), [How TTL works](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/howitworks-ttl.html)).
- Updating an expired-but-undeleted item's TTL to a future time (or removing it) prevents deletion ([How TTL works](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/howitworks-ttl.html)).
- TTL deletes go to Streams as service deletions: `userIdentity.type = "Service"`, `userIdentity.principalId = "dynamodb.amazonaws.com"`; Lambda event filtering uses `{"userIdentity":{"type":["Service"],"principalId":["dynamodb.amazonaws.com"]}}` ([Streams and TTL](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/time-to-live-ttl-streams.html)).
- `UpdateTimeToLive` request `{"TableName","TimeToLiveSpecification":{"AttributeName","Enabled"}}`; response `{"TimeToLiveSpecification":{"AttributeName","Enabled"}}`. "It can take up to one hour for the change to fully process. Any additional `UpdateTimeToLive` calls for the same table during this one hour duration result in a `ValidationException`." Documented errors: `InternalServerError` 500, `LimitExceededException` 400, `ResourceInUseException` 400, `ResourceNotFoundException` 400 ([UpdateTimeToLive](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_UpdateTimeToLive.html)).
- `TimeToLiveStatus` enum `ENABLING | DISABLING | ENABLED | DISABLED`; `TimeToLiveAttributeName` length 1-255; both `Enabled` and `AttributeName` required on update ([Smithy model](https://github.com/aws/api-models-aws/blob/main/models/dynamodb/service/2012-08-10/dynamodb-2012-08-10.json)). `DescribeTimeToLive` omits `AttributeName` when disabled; the Terraform schema comment confirms "AWS requires the attribute name to be set when disabling TTL but does not return it" ([table.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/dynamodb/table.go)).

### 5.2 What Local does (observed)

- `UpdateTimeToLive` / `DescribeTimeToLive` are implemented and immediate: `{"TimeToLiveDescription":{"TimeToLiveStatus":"DISABLED"}}`, then after enabling `{"TimeToLiveDescription":{"TimeToLiveStatus":"ENABLED","AttributeName":"ExpiresAt"}}`. The state is persisted across restart with `-dbPath`.
- Local validation messages: enabling when already enabled -> 400 `com.amazon.coral.validate#ValidationException` "TimeToLive is already enabled"; enabling a different attribute while active -> "TimeToLive is active on a different AttributeName". Disable then re-enable immediately succeeds (no one-hour lock).
- **Local does delete expired items.** Items with `ExpiresAt` in the past put into a table with TTL enabled disappear from `Scan` (physically: `ScannedCount` drops). Measured with 0.25 s polling: 1.06 s, 8.12 s, 7.83 s; an item expiring 8 s in the future was gone at 8.09 s. Enabling TTL on a table holding an already-expired item removed it within 3 s. Sweep cadence inferred from the four timestamps is roughly every 10 s (UNVERIFIED; not documented).
- Expired-but-unswept items are returned by `GetItem` (not filtered on read), matching AWS.
- The sweeper uses the host wall clock; no flag or env var to inject a clock was found. understudy's controllable clock therefore cannot drive Local's TTL.
- **TTL deletes produce a malformed stream record.** With streams `NEW_AND_OLD_IMAGES` the sweep emitted `"eventName":"UNKNOWN_TO_SDK_VERSION"` with `OldImage` and no `userIdentity`, instead of AWS's `REMOVE` + `userIdentity` Service/`dynamodb.amazonaws.com` (observed; likely an SDK-v2 enum gap inside Local). Any Streams-to-Lambda stretch work that relies on Local's own TTL would hand Lambda a wrong event.
- The README says "2017-04-13 (1.11.119) Add TTL implementation" ([README.txt in image](https://hub.docker.com/r/amazon/dynamodb-local)); nothing in the official docs describes the sweep behavior.

### 5.3 Prior art

LocalStack keeps TTL specs in its own store (`state.ttl_specifications`) and runs its own `ExpiredItemsWorker`: for each enabled table it calls `Scan` with `FilterExpression="#ttl <= :threshold"` and `BatchWriteItem` deletes in 25s, exposed also as `DELETE /_aws/dynamodb/expired` ([provider.py](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/dynamodb/provider.py)). That loop issues one unpaginated `Scan` per table (reads at most 1 MB per sweep), so a correct shim must follow `LastEvaluatedKey`.

---

## 6. Tags, continuous backups / PITR, SSE, table class, warm throughput

- Tags and PITR are absent from Local (section 4). `TagResource`, `UntagResource`, `ListTagsOfResource` take a `ResourceArn` (length 1-1283) and tags are `Key` 1-128 chars, `Value` 0-256 chars, `ListTagsOfResource` paginates with `NextToken` ([Smithy model](https://github.com/aws/api-models-aws/blob/main/models/dynamodb/service/2012-08-10/dynamodb-2012-08-10.json)). `CreateTable` also accepts `Tags`, and the Terraform provider relies on that (no separate `TagResource` at create) (section 7).
- `DescribeContinuousBackups` response shape: `{"ContinuousBackupsDescription":{"ContinuousBackupsStatus":"ENABLED|DISABLED","PointInTimeRecoveryDescription":{"EarliestRestorableDateTime":n,"LatestRestorableDateTime":n,"PointInTimeRecoveryStatus":"ENABLED|DISABLED","RecoveryPeriodInDays":n}}}`. "Continuous backups are `ENABLED` on all tables at table creation." `LatestRestorableDateTime` "is typically 5 minutes before the current time"; recovery period 1-35 days. Errors: `TableNotFoundException` (400), `InternalServerError` (500) ([DescribeContinuousBackups](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_DescribeContinuousBackups.html), [model](https://github.com/aws/api-models-aws/blob/main/models/dynamodb/service/2012-08-10/dynamodb-2012-08-10.json): `RecoveryPeriodInDays` range 1-35; `UpdateContinuousBackups` may raise `ContinuousBackupsUnavailableException`, `TableNotFoundException`).
- The exact PITR-disabled response body on real AWS (is `PointInTimeRecoveryDescription` present with status `DISABLED`?) is UNVERIFIED against the live service; the Terraform helper `findPITRByTableName` treats a nil `PointInTimeRecoveryDescription` as an empty result, so the shim should always include it ([find.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/dynamodb/find.go)).
- `SSEDescription` = `{Status: ENABLING|ENABLED|DISABLING|DISABLED|UPDATING, SSEType: AES256|KMS, KMSMasterKeyArn, InaccessibleEncryptionDateTime}`; `TableClassSummary` = `{TableClass: STANDARD|STANDARD_INFREQUENT_ACCESS, LastUpdateDateTime}`; `TableWarmThroughputDescription` = `{ReadUnitsPerSecond, WriteUnitsPerSecond, Status: TableStatus}` where `TableStatus` includes `CREATING, UPDATING, DELETING, ACTIVE, INACCESSIBLE_ENCRYPTION_CREDENTIALS, ARCHIVING, ARCHIVED, REPLICATION_NOT_AUTHORIZED` ([model](https://github.com/aws/api-models-aws/blob/main/models/dynamodb/service/2012-08-10/dynamodb-2012-08-10.json)).
- The provider treats warm-throughput values of exactly 12000 read / 4000 write (and 1/1) as AWS defaults and hides them from state ([table.go `flattenTableWarmThroughput`](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/dynamodb/table.go)). Returning `{12000, 4000, ACTIVE}` converged with no diff in my run (observed).

---

## 7. Terraform `aws_dynamodb_table` against raw Local

Provider tested: hashicorp/aws **6.68.0** (the latest release at research time per `gh api repos/hashicorp/terraform-provider-aws/releases/latest`), Terraform 1.16.0. Sources: [table.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/dynamodb/table.go), [wait.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/dynamodb/wait.go), [status.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/dynamodb/status.go), [find.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/dynamodb/find.go), [tags_gen.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/dynamodb/tags_gen.go), [htags.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/provider/interceptors/htags.go).

### 7.1 Call sequence (source, confirmed by a three-table apply/plan/destroy run)

Provider config used: `endpoints { dynamodb = <url> }` with `skip_credentials_validation`, `skip_requesting_account_id`, `skip_metadata_api_check`, `skip_region_validation`, and a fake access key. The SDK sends `Accept-Encoding: identity`, `X-Amz-Target: DynamoDB_20120810.<Op>`, `Content-Type: application/x-amz-json-1.0`.

- **Create**: `CreateTable` (input includes `Tags`, `BillingMode`, `KeySchema`, `AttributeDefinitions`, `GlobalSecondaryIndexes`, `StreamSpecification`, `SSESpecification`, `TableClass`, `DeletionProtectionEnabled`, `OnDemandThroughput`, `WarmThroughput`); `waitTableActive` polls `DescribeTable` (pending CREATING/UPDATING, target ACTIVE, `Delay` 5 s, `MinTimeout` 1 s, `ContinuousTargetOccurence` 2); `waitTableWarmThroughputActive` polls `DescribeTable` for `WarmThroughput.Status`; per-GSI `waitGSIActive`; if `ttl.enabled`: `UpdateTimeToLive` then poll `DescribeTimeToLive` until `ENABLED`; if `point_in_time_recovery.enabled`: `UpdateContinuousBackups` (retries on `ContinuousBackupsUnavailableException` containing "Backups are being enabled") then poll `DescribeContinuousBackups` until `ENABLED`; replicas via `UpdateTable` (out of scope). Then the Read below.
- **Read**: `DescribeTable`; `DescribeContinuousBackups` (an error with code `UnknownOperationException` or `TableNotFoundException` is tolerated and PITR is read as disabled); `DescribeTimeToLive` (any error fails the read); `ListTagsOfResource` with the table ARN (through the transparent-tagging interceptor); if `SSEDescription` is present, a KMS `DescribeKey` with `KeyId: alias/aws/dynamodb` (`clearSSEDefaultKey`, observed payload).
- **Update**: `UpdateTable` (billing mode, throughput, GSI create/update/delete in phases, stream cycle off/on, `TableClass`, `DeletionProtectionEnabled`, SSE, warm and on-demand throughput), `UpdateTimeToLive`, `UpdateContinuousBackups`, `TagResource`/`UntagResource` then `ListTagsOfResource` polled until tags match (`waitTagsPropagated`, `ContinuousTargetOccurence` 2).
- **Delete**: `DeleteTable` (retries `ResourceInUseException`, `LimitExceededException` containing "simultaneously"); a `ResourceNotFoundException` whose message contains "Requested resource not found: Table: " counts as already gone; then `waitTableDeleted` polls `DescribeTable` until `ResourceNotFoundException`.
- Observed call counts for one apply + refresh + destroy of three tables: `CreateTable` 3, `DeleteTable` 3, `DescribeTable` 28, `DescribeTimeToLive` 10, `DescribeContinuousBackups` 10, `ListTagsOfResource` 9, `UpdateTimeToLive` 1, `UpdateContinuousBackups` 1, KMS `DescribeKey` 3.
- Create wall time is 6-7 s per table because of the provider's 5 s `Delay`; this is provider-side and cannot be shortened by the emulator.
- Not used by the plain table resource: `DescribeEndpoints`, `DescribeLimits`. `DescribeKinesisStreamingDestination`, `DescribeContributorInsights`, `GetResourcePolicy`, `DescribeTableReplicaAutoScaling`, `ImportTable`, `RestoreTableFromBackup`, `RestoreTableToPointInTime` belong to other resources or optional blocks (`import_table`, `restore_*`, `replica`, kinesis destination, resource policy), all unsupported by Local and outside phase 1.

### 7.2 What breaks against raw Local (observed)

1. **`terraform apply` fails after ~2.5 minutes** for even a minimal table: `Error: waiting for update AWS DynamoDB Table (tf-plain): couldn't find resource (21 retries)`. Cause: `statusTableWarmThroughput` returns `nil, "", nil` when `DescribeTable` has no `WarmThroughput`, and the state waiter counts that as not found ([status.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/dynamodb/status.go), [wait.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/dynamodb/wait.go)). Local never returns `WarmThroughput`. The shim must add `"WarmThroughput":{"ReadUnitsPerSecond":12000,"WriteUnitsPerSecond":4000,"Status":"ACTIVE"}` to every table description.
2. `point_in_time_recovery { enabled = true }` fails at create: `updating PITR: ... api error UnknownOperationException` (`UpdateContinuousBackups` is not tolerated on the write path, only `DescribeContinuousBackups` is tolerated on read). Shim needed.
3. Tags are silently lost, not an error: `HTags.ListTags` special-cases DynamoDB and swallows `UnknownOperationException` with message containing "Tagging is not currently supported in DynamoDB Local." ([htags.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/provider/interceptors/htags.go)). State shows `tags = {}`, every plan shows `+ tags`, and any tag change on update would fail on `TagResource`. Shim needed for read-fidelity.
4. `table_class = "STANDARD_INFREQUENT_ACCESS"`: `CreateTable` accepts it, `DescribeTable` returns no `TableClassSummary` (provider defaults to STANDARD, giving a permanent diff), and `UpdateTable` with only `TableClass` fails with `ValidationException: Nothing to update`.
5. `server_side_encryption { enabled = true }`: Local returns no `SSEDescription`, so the block reads back empty (permanent diff `+ server_side_encryption`). When the shim does return `SSEDescription`, the provider issues KMS `DescribeKey` for `alias/aws/dynamodb` ([table.go `clearSSEDefaultKey`](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/dynamodb/table.go)), so understudy's KMS service must answer that alias or the call must fail softly (the provider ignores the error and keeps the ARN).
6. ARNs: `arn`, `stream_arn` come straight from `TableArn`/`LatestStreamArn` and would read `arn:aws:dynamodb:ddblocal:000000000000:...`, wrong for IAM policies and any module that builds ARNs from the account id. Rewrite required.

### 7.3 Validated shim set

With a scratch proxy in front of Local (research only, not repo code) that implemented exactly: add `WarmThroughput`, add `TableClassSummary`/`SSEDescription` from stored `CreateTable`/`UpdateTable` inputs, rewrite `TableArn`/`IndexArn`/`LatestStreamArn` to `arn:aws:dynamodb:<region>:<account>:...`, serve `ListTagsOfResource`/`TagResource`/`UntagResource` from a map seeded from `CreateTable.Tags`, serve `DescribeContinuousBackups`/`UpdateContinuousBackups` from a map, and recompute `x-amz-crc32` on rewritten bodies, a config with a plain table, a full table (GSI, TTL, PITR, streams, `STANDARD_INFREQUENT_ACCESS`, two tags) and an SSE table gave: `apply` success (3 resources, 6-7 s), second `plan` "No changes", `destroy` success (observed, hashicorp/aws 6.68.0). TTL and Streams requests were passed straight to Local in that run.

---

## 8. Streams

Supported by Local on the same port; operations use `X-Amz-Target: DynamoDBStreams_20120810.<Op>` (observed: `ListStreams`, `DescribeStream`, `GetShardIterator`, `GetRecords`). `DescribeStream` supports `ShardFilter` since 3.2.0 ([README.txt in image](https://hub.docker.com/r/amazon/dynamodb-local)).

Observed with `StreamViewType` `NEW_AND_OLD_IMAGES`:

- `CreateTable`/`DescribeTable` return `"StreamSpecification":{"StreamEnabled":true,"StreamViewType":"NEW_AND_OLD_IMAGES"}`, `"LatestStreamLabel":"2026-10-08T04:40:16.197"`, `"LatestStreamArn":"arn:aws:dynamodb:ddblocal:000000000000:table/Streamy/stream/2026-10-08T04:40:16.197"`. AWS stream label format in its docs example is `2021-12-10T00:00:00.000` ([Streams and TTL](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/time-to-live-ttl-streams.html)), same shape.
- One shard, id like `shardId-00000001791434416204-cb870c62`, sequence numbers are small zero-padded integers (`000000000000000000014`).
- Shard iterator is `<streamArn>|001|<base64>`.
- Record shape: `{"eventID","eventName":"INSERT|REMOVE|MODIFY","eventVersion":"1.1","eventSource":"aws:dynamodb","awsRegion":"ddblocal","dynamodb":{"ApproximateCreationDateTime":1791434400.000,"Keys","NewImage","OldImage","SequenceNumber","SizeBytes","StreamViewType"}}`. `ApproximateCreationDateTime` is floored to the minute (observed: records written at 04:40:2x show `...400` = 04:40:00). `awsRegion` is `ddblocal` and must be rewritten.
- TTL-driven removals emit `"eventName":"UNKNOWN_TO_SDK_VERSION"` (section 5.2).
- Table delete does not require stream cleanup from the caller.

Streams to Lambda is a stretch goal in the brief; the poller that feeds the event source mapping has to read through the proxy (`GetRecords` with the rewritten ARN) and must emit AWS's Lambda DynamoDB event format; that format is covered by the Lambda events research file, not here.

---

## 9. HTTP-level details a transparent proxy must preserve

### 9.1 `x-amz-crc32`

- Local sets `x-amz-crc32: <decimal uint32>` on **200 responses only**; 400 error responses carry no such header (observed). The value is CRC-32/IEEE of the exact response body bytes: for `{"TableNames":["Locks"]}` Local sent `641657579`, Python `zlib.crc32` of the saved body gave `641657579` (observed).
- Local did not gzip even with `Accept-Encoding: gzip` (observed), so the checksum is over plain bytes.
- AWS SDK for Go v2 DynamoDB client: validates only when the header is present; a non-numeric value is a `DeserializationError`; a mismatch surfaces as `response did not match expected checksum, <expect>, <actual>` when the body is closed. For gzip responses the checksum is of the gzipped bytes, "not the decompressed response bytes". Disabled with `DisableValidateResponseChecksum`; gzip is off by default (`EnableAcceptEncodingGzip`) ([checksum.go](https://github.com/aws/aws-sdk-go-v2/blob/main/service/dynamodb/internal/customizations/checksum.go), [doc.go](https://github.com/aws/aws-sdk-go-v2/blob/main/service/dynamodb/internal/customizations/doc.go)). The Terraform provider's requests carried `Accept-Encoding: identity` (observed).
- botocore: `CRC32Checker` (legacy retry mode, `crc32body: x-amz-crc32` in the dynamodb policy, `max_attempts` 10) and `RetryDDBChecksumError` (standard/adaptive mode) compare `crc32(response.content) & 0xFFFFFFFF` with `int(header)`; a mismatch is a retryable `ChecksumError`; an absent header skips the check ([retryhandler.py](https://github.com/boto/botocore/blob/develop/botocore/retryhandler.py), [retries/special.py](https://github.com/boto/botocore/blob/develop/botocore/retries/special.py), [_retry.json](https://github.com/boto/botocore/blob/develop/botocore/data/_retry.json)).
- AWS SDK for JavaScript v3: I found no DynamoDB `x-amz-crc32` validation (GitHub code search for `crc32` in `aws/aws-sdk-js-v3` returned only unrelated files). UNVERIFIED that none exists.
- Rule for the proxy: whenever any response body byte changes (ARN rewrite, injected fields), recompute CRC32 over the final bytes and set `Content-Length`; never forward Local's stale header. Synthesized 200 responses (tags, PITR, TTL) must carry a correct header too, because clients that validate will otherwise fail intermittently or never.

### 9.2 Request and response headers

- Request: `POST /` with `X-Amz-Target: DynamoDB_20120810.<Op>` (or `DynamoDBStreams_20120810.<Op>`), `Content-Type: application/x-amz-json-1.0`, SigV4 `Authorization`. Local ignores the signature but needs a parsable `Credential=<key>/<date>/<region>/dynamodb/aws4_request` with an alphanumeric key.
- Response headers from Local: `x-amzn-RequestId: <uuid>`, `Content-Type: application/x-amz-json-1.0`, `Content-Length`, `x-amz-crc32` (200 only), `Server: Jetty(12.1.11)`, `Date`. No `x-amz-id-2` or `x-amzn-ErrorType` header was observed on errors; the error code is in the JSON `__type` field (real-service header behavior UNVERIFIED).
- Unknown operation: 400 `UnknownOperationException` (not 501).

---

## 10. Prior art summary (LocalStack, archived)

LocalStack ran a similar architecture: Local launched with `-port`, `-inMemory` or `-dbPath`, optional `-sharedDb`, `-delayTransientStatuses`, `-optimizeDbBeforeStartup`, a javaagent patch, Java 21, `DDB_LOCAL_TELEMETRY=0`; the framework rewrote the `Authorization` credential per account+region, kept tags, TTL specs, stream plumbing, replicas and SSE descriptions in its own store, and regex-rewrote ARNs in responses ([server.py](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/dynamodb/server.py), [provider.py](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/dynamodb/provider.py), [utils.py](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/dynamodb/utils.py)). The repo is archived (`gh api repos/localstack/localstack` -> `archived: true`, license `NOASSERTION`), so use it as a map of what is needed, not as a source to copy.

---

## Implications for understudy

Decisions and constraints, ordered by impact. Each is a recommendation for the architecture author, not yet an ADR.

1. **Never bundle; pull at runtime; make the license visible.**
   - Treat `amazon/dynamodb-local` as a sealed dependency: pull by digest (`sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab` is 3.3.1), no `docker save`, no derived image, no patching, no copying files out of it.
   - Offer `public.ecr.aws/aws-dynamodb-local/aws-dynamodb-local` as a fallback registry (same digest) to dodge Docker Hub rate limits.
   - On first pull print the license notice and link; document that the license requires an AWS account in good standing. Request legal review before the first public release (section 2). Proposed notice text: [dynamodb-local-terms.md](dynamodb-local-terms.md) section 5.1.
   - Fail with a clear, specific error when the pull is impossible or AWS withdraws the image, and leave the other services working.
2. **One Local container, native partitioning via credential rewrite.** Rewrite the `Authorization` `Credential=` of every forwarded request to `<accountId>/<date>/<region>/dynamodb/aws4_request` with the emulator account id (12 digits, alphanumeric, accepted) and a region validated against understudy's own region list. Local then isolates (account, region) with no per-account containers. Never forward a client-chosen region string unvalidated, because it becomes part of a file name under `-dbPath`. Use `-inMemory` by default (partitioning still applies) and `-dbPath` only when persistence is requested.
3. **Rewrite every ARN and region leaving Local.** Replace `arn:aws:dynamodb:ddblocal:000000000000:` with `arn:<partition>:dynamodb:<region>:<account>:` in `TableArn`, `IndexArn`, `LatestStreamArn`, `StreamArn`, `Streams[].StreamArn`, `NextShardIterator`/`ShardIterator` prefixes (they embed the stream ARN), and `awsRegion` in stream records. Parse JSON and rewrite typed fields rather than regex the body. Inbound, accept table ARNs as `TableName` (Local already tolerates this) and translate the account/region in `ResourceArn` for tag calls to the request's own scope.
4. **Own the missing control plane with typed state per (account, region, table).**
   - Table metadata store: tags (seeded from `CreateTable.Tags`), TTL spec, PITR spec, `TableClass` (+ `LastUpdateDateTime`), `SSESpecification`, table id (generate a UUID; Local omits `TableId`).
   - On every `CreateTable`, `DescribeTable`, `UpdateTable`, `DeleteTable` response inject `WarmThroughput {12000, 4000, ACTIVE}`, `TableClassSummary`, `SSEDescription`, `TableId`; without `WarmThroughput` `terraform apply` hangs ~2.5 min and fails (section 7.2).
   - Handle `UpdateTable` containing only shim-owned fields locally and strip those fields before forwarding the remainder; if nothing remains, return the shimmed `DescribeTable`-shaped `TableDescription` instead of letting Local answer "Nothing to update".
   - Return `TableStatus: DELETING` on `DeleteTable` to match AWS (Local returns ACTIVE; Terraform tolerates both).
   - Translate Local's `ResourceNotFoundException` text? Terraform only string-matches the message on delete, tolerating either; AWS text is "Requested resource not found" if fidelity tests compare messages.
5. **Implement the full tag trio exactly.** `TagResource`, `UntagResource`, `ListTagsOfResource` with limits from the Smithy model (key 1-128, value 0-256, `NextToken` pagination), errors `ResourceNotFoundException`/`ResourceInUseException`/`LimitExceededException`, plus `Tags` on `CreateTable`. Terraform polls `ListTagsOfResource` twice in a row to confirm equality, so reads must be strongly consistent. Tag state belongs in the snapshot.
6. **TTL runs entirely in the shim, on the emulator clock; Local never learns about it.**
   - Do not forward `UpdateTimeToLive`/`DescribeTimeToLive` to Local: Local's sweeper uses the wall clock, runs about every 10 s, and its stream record for TTL deletes is wrong (`UNKNOWN_TO_SDK_VERSION`, no `userIdentity`). Keeping the spec out of Local disables its sweeper for free.
   - Sweep: for each enabled table, `Scan` with `FilterExpression "#ttl <= :now"` (and `attribute_type` Number guard) following `LastEvaluatedKey`, then delete each key with a `DeleteItem` conditioned on the TTL attribute still being `<= :now` so a concurrent TTL refresh wins (AWS: updating an expired item "before the item is deleted" saves it). Drive the sweep off the controllable clock (frozen, stepped, scaled); expose a "sweep now" admin hook for tests (LocalStack's `/_aws/dynamodb/expired` is the equivalent).
   - Emulate the status machine: `ENABLED`/`DISABLED` immediately is fine for Terraform (it accepts pending ENABLING/DISABLING); optionally enforce AWS's one-change-per-hour rule on the emulator clock, noting that `ValidationException` is raised for repeat changes and that Local's own messages ("TimeToLive is already enabled", "TimeToLive is active on a different AttributeName") are observed, with the real-service text UNVERIFIED.
   - Streams: when streams are enabled, TTL deletions must emit `REMOVE` records with `userIdentity {"type":"Service","principalId":"dynamodb.amazonaws.com"}`. Local will not add it, so the stream path must tag these deletes (e.g. the sweeper marks its own deletes and the stream-record rewriter attaches `userIdentity` for those sequence numbers).
7. **PITR is a stored flag plus synthesized describe.** `UpdateContinuousBackups` stores `PointInTimeRecoveryEnabled` and `RecoveryPeriodInDays` (1-35; default 35 when enabled without a value, UNVERIFIED); `DescribeContinuousBackups` always returns `ContinuousBackupsStatus: ENABLED` and a `PointInTimeRecoveryDescription` with `PointInTimeRecoveryStatus`, and when enabled `EarliestRestorableDateTime`, `LatestRestorableDateTime` (clock now minus 5 min, floored at enable time) and `RecoveryPeriodInDays`. Throw `TableNotFoundException` (not `ResourceNotFoundException`) for missing tables on these two calls, per the model. Restore operations stay unimplemented and fail loudly.
8. **Unimplemented-operation policy for Local gaps.** Local answers `UnknownOperationException` (400) for what it lacks. understudy's brief demands loud AWS-format failures that say "unimplemented", so intercept operations outside the supported list (`DescribeKinesisStreamingDestination`, `DescribeContributorInsights`, `GetResourcePolicy`, import/export/backup/restore, replica auto scaling, etc.) before forwarding and return understudy's own error rather than Local's generic text.
9. **`x-amz-crc32` is mandatory on every 200.** Compute it over final bytes for every proxied-and-modified and every synthesized response; omit on non-200 like Local, or include (both are safe for the SDKs reviewed). Advertise no gzip: strip `Accept-Encoding` toward Local or send `identity` (Local did not compress anyway), since a client that sent `gzip` would validate the checksum over compressed bytes.
10. **Fault injection, journal and clock integration points.**
    - The proxy sees every call, so the journal and fault rules apply before and after the forward. Inject AWS-shaped `ProvisionedThroughputExceededException`, `ThrottlingException`, `TransactionConflictException` (Local never raises it) and `ReplicatedWriteConflictException` from the fault engine; botocore retries `ReplicatedWriteConflictException` and checksum failures per its dynamodb policy ([_retry.json](https://github.com/boto/botocore/blob/develop/botocore/data/_retry.json)).
    - Note that item-level time (e.g. `ExpiresAt < :now` in application conditions) is evaluated by the client's `:now`, not by Local, so the emulator clock only matters for TTL and Streams timestamps here.
11. **Stable container lifecycle.**
    - Start: `java` entrypoint args `-jar DynamoDBLocal.jar -port 8000 -disableTelemetry` + (`-inMemory` | `-dbPath /data`), env `DDB_LOCAL_TELEMETRY=0` (the value must be exactly `0`; each opt-out alone is sufficient, passing both is belt and braces, see section 1.4), publish `127.0.0.1:0:8000`, read the port from the Docker API, label every container, set a `--memory` bound (idle ~150 MiB), readiness = any HTTP response to `GET /` (400 `MissingAuthenticationToken` is the healthy answer; cold start ~1.2 s).
    - Persistence: for snapshots, don't copy live SQLite files; rebuild by shim-replay (`DescribeTable` + paginated `Scan` per table) into the understudy snapshot format, or stop Local before copying `-dbPath`. Copy-while-running safety is UNVERIFIED.
    - Restart changes the published port unless pinned; keep the proxy's upstream address updated.
    - Write access: bind-mounted data dirs must be writable by uid 1000.
12. **Table-name and validation fidelity.** Local rejects table and index names shorter than 3 characters with the same text as AWS, and is case-insensitive on table names while AWS is case-sensitive; the shim should enforce case-sensitive identity itself (two tables `Authors` and `authors` must coexist on AWS; Local errors). Mapping to distinct internal Local names (for example a stable suffix or escaped name) is one option; behavior then needs the name rewritten on the way out. Choose and record in an ADR. Also remember the Smithy `TableName` pattern `^[a-zA-Z0-9_.-]+$`, length 3-255 ([model](https://github.com/aws/api-models-aws/blob/main/models/dynamodb/service/2012-08-10/dynamodb-2012-08-10.json)).
13. **Known fidelity gaps to document, not hide.** No provisioned-throughput enforcement or throttling (inject via faults), no `TransactionConflictException`, sequential `Scan` ignoring segments, `ExecuteStatement` ignores `Limit`, immediate `ItemCount`/`TableSizeBytes`, GSI sizes count whole items, no vector indexes, no global tables, Streams shard layout differs (single shard).
14. **Test plan hooks.** Cheap regression for the central risk: a Terraform conformance case that applies the plain, full and SSE tables from section 7.3 and asserts apply, empty second plan and clean destroy; Go SDK v2 and boto3 cases that confirm CRC32 validation passes on rewritten responses; a TTL case on a frozen then advanced clock; a partition-isolation case across two access keys and two regions.

### Open questions (UNVERIFIED)

- Legal reading of "in connection with the Services" and of using Local as the backend of another emulator (section 2).
- Resolved by G5: `DDB_LOCAL_TELEMETRY=0` alone is honored and Local makes no outbound attempt with telemetry disabled (section 1.4). Remaining open items on this topic are in [dynamodb-local-terms.md](dynamodb-local-terms.md) section 6.
- Exact real-service text for repeat `UpdateTimeToLive` and for enabling-when-already-enabled errors; exact `DescribeContinuousBackups` body for a never-enabled PITR table.
- Local's TTL sweep period (inferred ~10 s from four samples).
- Whether odd region strings create odd files under `-dbPath`.
- Whether SQLite files are safe to copy while Local runs.
- JS v3 `x-amz-crc32` handling for DynamoDB.
- Whether the ECR Public mirror is an officially documented channel.
