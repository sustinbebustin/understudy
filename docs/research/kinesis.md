# Amazon Kinesis Data Streams: primary-source research

Scope: Kinesis Data Streams for roadmap item 8 (second half). Covers the wire protocol (awsJson1_1, the legacy CBOR variant the Java SDK sends, HTTP/2), the stream lifecycle, shards and the hash key space, sequence numbers and shard iterators, records and their limits, the error catalogue, enhanced fan-out, the Lambda Kinesis event, the EventBridge Kinesis target, and what Terraform needs. Sources, all retrieved 2026-10-25: the Smithy model `com.amazonaws.kinesis#Kinesis_20131202` at api-models-aws `1cfdfb4bb9ea` ([model](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/kinesis/service/2013-12-02/kinesis-2013-12-02.json)); the [Kinesis Data Streams API Reference](https://docs.aws.amazon.com/kinesis/latest/APIReference/) and [Developer Guide](https://docs.aws.amazon.com/streams/latest/dev/) (read in their Markdown form); the Lambda Developer Guide Kinesis pages; the EventBridge API Reference; LocalStack's AWS-validated Kinesis tests at `localstack@8b9a79f`; hashicorp/terraform-provider-aws v6.68.0 `internal/service/kinesis`; client source of aws-sdk-java-v2, amazon-kinesis-client (KCL), aws-sdk-go-v2, aws-sdk-js-v3 and botocore at the commits named inline; and kinesalite as an emulator hint. Nothing here contacted an AWS API endpoint.

## 0. How to read this

| Tag | Meaning |
|---|---|
| [S] | The Smithy model linked above. Shapes are cited by id (`com.amazonaws.kinesis#PutRecordInput`). |
| [D] | AWS documentation, linked inline. |
| [R] | Third-party-recorded: an assertion or snapshot in LocalStack's suite whose test has a `last_validated_date` in `tests/aws/services/kinesis/test_kinesis.validation.json` at `localstack@8b9a79f`. Snapshots mask ids, hash keys and sequence numbers, so they fix shapes and texts, not values. |
| [R-weak] | A LocalStack test marked `aws.validated` whose id is absent from the validation file. |
| [C] | Client or provider source at a pinned commit. |
| [E] | Another emulator's source (kinesalite `05c2a3e9e679`, LocalStack's provider). Hints only, never proof. |

Abbreviations: **TF** is hashicorp/terraform-provider-aws v6.68.0 `internal/service/kinesis/`; **LS** is `localstack@8b9a79f tests/aws/services/kinesis/`; **JV2** is aws-sdk-java-v2 at `c7cfad4db4e5c9d203034c65ece67f3e15110ef3`; **KCL** is awslabs/amazon-kinesis-client at `a43767aec4d9979d25f783b5a3e312aadd2844cb`; **KL** is mhart/kinesalite at `05c2a3e9e6794e49d2c23ee2feafb4f71797ef58`.

A behavior no source fixes is marked UNVERIFIED with a register id `KIN-NN` (section 12); section 14 gives the recording that settles each one.

## 1. Findings that change the design

1. **Java clients send CBOR over HTTP/2 with prior knowledge.** aws-sdk-java-v2 generates Kinesis with `"protocol": "cbor"` and `"usePriorKnowledgeForH2": true` (JV2 `services/kinesis/src/main/resources/codegen-resources/customization.config:L6-L8, L54`), so `KinesisAsyncClient` on Netty, and therefore KCL, opens cleartext `http://` endpoints with an HTTP/2 preface and posts `application/x-amz-cbor-1.1` bodies (section 2.3, 2.4). understudy's listener speaks HTTP/1 only today (`internal/edge/edge.go`, `Handler.Server` sets no `Protocols`). Without h2c and CBOR, no Java async client reaches understudy at all.
2. **KCL defaults to enhanced fan-out** (`SubscribeToShard`): with no retrieval config it builds a `FanOutConfig` (KCL `amazon-kinesis-client/src/main/java/software/amazon/kinesis/retrieval/RetrievalConfig.java:L190-L196`). While `SubscribeToShard` is unbound, KCL users must configure polling (section 7.3).
3. **Terraform calls `IncreaseStreamRetentionPeriod` with the current value on every create.** `retention_period` defaults to 24 and the create path calls the operation whenever it is above 0 (TF `stream.go:L169-L174, L261-L276`), on a stream that is already at 24 hours. The API reference says the new value "Must be more than the current retention period" ([IncreaseStreamRetentionPeriod](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_IncreaseStreamRetentionPeriod.html)); AWS therefore accepts an equal value, or every Terraform create would fail (inferred; KIN-06).
4. **`CreateStream` with only a name makes an on-demand stream with 4 shards.** The guide's "on-demand" example sets only the name ([create stream](https://docs.aws.amazon.com/streams/latest/dev/kinesis-using-sdk-java-create-stream.html)), and AWS returned four shards for `create_stream(StreamName=...)` [R] (LS `test_kinesis.snapshot.json:L2-L47`, validated 2022-08-26). Terraform never sends that form: it defaults to `PROVISIONED` and always sends `ShardCount` (TF `stream.go:L229-L231, L726-L733`).
5. **`ResourceNotFoundException` names the stream and account, even when addressed by ARN**: `Stream wrong-stream-name under account 111111111111 not found.` [R] (LS `test_kinesis.snapshot.json:L230-L237`, a CBOR DescribeStream by `StreamARN`, validated 2025-09-16; the test skips `$..message` against LocalStack because "error message is wrong in Kinesis (returns the full ARN)", `test_kinesis.py:L817-L820`). The JSON text is assumed identical (inferred; KIN-05 step 02 records it). Almost every other message text is unrecorded (section 6).
6. **Hash ranges and sequence numbers have documented shapes.** The documented 3-shard stream starts shard `i` at `i * floor(2^128 / 3)` (section 3.3). Real sequence numbers are 56 decimal digits that encode shard creation time, shard index and a per-shard counter (section 3.4). Shard iterators are reusable for 5 minutes, "even after it is used" ([developing consumers](https://docs.aws.amazon.com/streams/latest/dev/developing-consumers-with-sdk.html)), so they are stateless position tokens.
7. **The model is newer than most references.** It carries `MaxRecordSizeInKiB` (records up to 10 MiB, default 1 MiB), `RecordDistributionStrategy` (`AUTO` makes `PartitionKey` optional), warm throughput and account settings, five `Channel` operations (S3 delivery), `DryRun` on the data plane and a reserved `StreamId` member on almost every input [S]. The 2026 model has 45 operations; scope decisions for each are in 13.1.
8. **No error shape has an `httpError` trait.** Every modeled error is `client` (400) except `InternalFailureException` (`server`, 500) [S]. `ValidationException` in this model means "you tried to invoke this API for a data stream with the on-demand capacity mode" (`com.amazonaws.kinesis#ValidationException` documentation), not generic constraint failure.
9. **AWS serves `SubscribeToShard` over HTTP/1.1.** boto3 (urllib3, HTTP/1.1 only) read events from it in a test validated 2025-08-19 [R] (LS `test_kinesis.py:L277-L330`), although the model lists only `h2` under `eventStreamHttp` [S].
10. **Throughput limits are load-dependent and are not reproduced.** Per-shard write (1 MiB/s, 1,000 records/s) and read (5 calls/s, 2 MiB/s) limits [D] are named coverage gaps; `ProvisionedThroughputExceededException` comes only from fault injection (13.7).

## 2. Wire protocol

### 2.1 awsJson1_1 routing and serialization

- Service shape `com.amazonaws.kinesis#Kinesis_20131202`, version `2013-12-02`, with `aws.protocols#awsJson1_1` (`http: ["http/1.1", "h2"]`, `eventStreamHttp: ["h2"]`), `aws.auth#sigv4` name `kinesis`, `aws.api#service` `sdkId: Kinesis`, `arnNamespace: kinesis`, `endpointPrefix: kinesis`, and an unused `smithy.api#xmlNamespace` `http://kinesis.amazonaws.com/doc/2013-12-02` [S]. There are no `endpoint` or `hostPrefix` traits.
- `POST /`, `Content-Type: application/x-amz-json-1.1`, `X-Amz-Target: Kinesis_20131202.<Operation>` ([CreateStream example](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_CreateStream.html)). Operations with `smithy.api#Unit` output (CreateStream, DeleteStream, the retention, tag, encryption, reshard and resource-policy writes) answer "HTTP 200 response with an empty HTTP body" (same page) [D].
- No member has `@timestampFormat` and no member has `@default` [S], so timestamps are epoch seconds on JSON. Doc examples print them in exponent notation: `"StreamCreationTimestamp": 1.468346745E9` ([DescribeStream](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_DescribeStream.html)), `"ApproximateArrivalTimestamp": 1.441215410867E9` ([GetRecords](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_GetRecords.html)). `Data` is a blob, base64 on JSON.
- Every operation carries `smithy.rules#staticContextParams` `OperationType`: `data` for GetRecords, GetShardIterator, PutRecord, PutRecords and SubscribeToShard, `control` for the other 40 [S].
- `@paginated`: ListStreams (`NextToken`/`Limit`, no `items`), ListStreamConsumers (`NextToken`/`MaxResults`), ListChannels [S]. ListShards has a `NextToken` but no trait, and the provider paginates it by hand (TF `list_pages.go:L15-L33`).
- Clients: aws-sdk-go-v2 `service/kinesis/v1.56.1` (the version TF pins) sets `options.Protocol = awsjson.New11(schemas.Kinesis_20131202)` ([api_client.go L217](https://github.com/aws/aws-sdk-go-v2/blob/service/kinesis/v1.56.1/service/kinesis/api_client.go#L217)); aws-sdk-js-v3 uses `AwsJson1_1Protocol` ([runtimeConfig.shared.ts L36](https://github.com/aws/aws-sdk-js-v3/blob/163a566ffba89915cade6109031c5892ea3193c4/clients/client-kinesis/src/runtimeConfig.shared.ts#L36)); botocore's metadata is `"protocol":"json"`, `"jsonVersion":"1.1"`, `"targetPrefix":"Kinesis_20131202"`, `"protocolSettings":{"h2":"eventstream"}` ([service-2.json](https://github.com/boto/botocore/blob/fecd1e19cf8e1c04b66480397455e1458867bf6d/botocore/data/kinesis/2013-12-02/service-2.json)) [C].

### 2.2 Endpoint rules and the custom endpoint

- Without a custom endpoint, a call that names a `StreamARN` goes to an account endpoint: `StreamARN arn:aws:kinesis:us-east-1:123:stream/test-stream` with `OperationType data` resolves to `https://123.data-kinesis.us-east-1.amazonaws.com`, `control` to `https://123.control-kinesis.us-east-1.amazonaws.com` (model `smithy.rules#endpointTests`) [S].
- With `Endpoint` set (`AWS_ENDPOINT_URL`), every test case resolves to it unchanged: "Custom Endpoint is specified" for `StreamARN`, `ConsumerARN` and `ResourceARN` inputs all give `https://example.com` [S]. understudy therefore sees every call on its own host and needs no account-prefixed host routing.
- The rules fail malformed ARNs client side ("Invalid ARN: Failed to parse ARN.", "Invalid ARN: The ARN was not for the Kinesis service, found: s3.", "Invalid ARN: Invalid region.", "Partition: aws from ARN doesn't match with partition name: aws-us-gov.") [S]. Those test cases carry no `Endpoint`, so whether a custom endpoint skips the ARN checks is not covered by them; LocalStack notes that an invalid ARN "triggers end-point-resolution in botocore and fails client side" (LS `test_kinesis.py:L222-L224`) [R-weak].

### 2.3 The CBOR variant (`application/x-amz-cbor-1.1`)

- The model declares only `awsJson1_1` [S]. The CBOR form is a Java SDK customization: `"customServiceMetadata": {"protocol": "cbor"}` (JV2 `customization.config:L6-L8`) and the content-type prefix `application/x-amz-cbor-` with protocol version 1.1 (JV2 `core/protocols/aws-cbor-protocol/src/main/java/software/amazon/awssdk/protocols/cbor/AwsCborProtocolFactory.java:L51`). Routing is the same `POST /` with `X-Amz-Target: Kinesis_20131202.<Op>`; LocalStack's AWS-validated CBOR client sends `content-type: constants.APPLICATION_AMZ_CBOR_1_1` and `x-amz-target: Kinesis_20131202.<operation>` (LS `test_kinesis.py:L85-L90`) [R].
- Opt-out: system property `aws.cborEnabled`, default `"true"` (JV2 `core/sdk-core/src/main/java/software/amazon/awssdk/core/SdkSystemSetting.java:L157-L161`), environment variable named after the enum constant, `CBOR_ENABLED` (same file, L304-L306). With it false the factory falls back to JSON (`AwsCborProtocolFactory.java:L68-L72, L80-L84`) [C].
- **Timestamps are epoch milliseconds under CBOR tag 1**, not seconds as RFC 8949 defines. The Java generator writes `writeTag(1)` then `writeNumber(instant.toEpochMilli())` (JV2 `core/protocols/aws-cbor-protocol/src/main/java/software/amazon/awssdk/protocols/cbor/internal/SdkCborGenerator.java:L44-L57`) and parses payload timestamps as `UNIX_TIMESTAMP_MILLIS`: "CBOR uses epoch millis for timestamps rather than epoch seconds with millisecond decimal precision like JSON protocols" (`AwsCborProtocolFactory.java:L87-L100`) [C]. AWS does the same: LocalStack patches cbor2 to divide tag-1 values by 1000 because "Kinesis ... does not use the number of seconds ... but uses milliseconds" (`localstack@8b9a79f localstack-core/localstack/aws/client.py:L189-L225`), and with that patch its AWS-validated tests find `StreamCreationTimestamp` within one minute of now (LS `test_kinesis.py:L718-L760`, validated 2024-07-30) and identical `ApproximateArrivalTimestamp` values over JSON and CBOR (L506-L549, validated 2024-07-04) [R]. The first of those tests also asserts that the bytes `StreamCreationTimestamp\xc1\xfb` (tag 1 followed by a float64) are absent from AWS's DescribeStream response (L751) [R].
- Blobs are CBOR byte strings: `PutRecord` with `Data` as bytes and `GetRecords` over CBOR return the same bytes (LS `test_kinesis.py:L775-L814`, validated 2024-07-31) [R].
- Errors over CBOR: status 400, body map `{"__type": "ResourceNotFoundException", "message": "Stream wrong-stream-name under account 111111111111 not found."}` (LS `test_kinesis.snapshot.json:L230-L237`; status asserted at `test_kinesis.py:L839`; validated 2025-09-16) [R]. `__type` is the bare shape name; the message member is lower-case `message`.
- The response `Content-Type` for CBOR answers, and whether integers are minimal-width, are UNVERIFIED (KIN-20).
- Senders: Java SDK v2 and KCL. Go v2, JS v3, botocore and the AWS CLI send JSON (2.1). LocalStack wrote its own CBOR client because "This serialization type is not available via botocore" (LS `test_kinesis.py:L42-L46`) [E].

### 2.4 HTTP/2

- The protocol trait lists `h2` for every operation and only `h2` for event streams [S].
- Java: the client builder sets `PROTOCOL = HTTP2` for services with h2 support and adds `PROTOCOL_NEGOTIATION = ALPN` only when `usePriorKnowledgeForH2` is false (JV2 `codegen/src/main/java/software/amazon/awssdk/codegen/poet/builder/BaseClientBuilderClass.java:L877-L887`); Kinesis sets it true (`customization.config:L54`) and its metadata has `"protocolSettings": {"h2": "eventstream"}` (JV2 `services/kinesis/src/main/resources/codegen-resources/service-2.json`). Netty's default negotiation is `ASSUME_PROTOCOL` (JV2 `http-client-spi/src/main/java/software/amazon/awssdk/http/SdkHttpConfigurationOption.java:L166-L167`), which for `HTTP2` installs the HTTP/2 client codec directly (JV2 `http-clients/netty-nio-client/src/main/java/software/amazon/awssdk/http/nio/netty/internal/ChannelPipelineInitializer.java:L117-L155`) [C]. So the async client speaks HTTP/2 with prior knowledge on `http://` endpoints too (inferred from that code path). Kinesis also gets 10 s read and write timeouts (JV2 `services/kinesis/src/main/java/software/amazon/awssdk/services/kinesis/internal/KinesisHttpConfigurationOptions.java:L28-L32`).
- KCL builds its client with `NettyNioAsyncHttpClient` and `Protocol.HTTP2` (KCL `amazon-kinesis-client/src/main/java/software/amazon/kinesis/common/KinesisClientUtil.java:L45-L51`) [C].
- Go 1.24 added `Server.Protocols`: "When Server.Protocols contains UnencryptedHTTP2, the server will accept HTTP/2 connections on unencrypted ports. The server can accept both HTTP/1 and unencrypted HTTP/2 on the same port", using prior knowledge only ([Go 1.24 release notes](https://go.dev/doc/go1.24)). No dependency is needed.
- AWS served `SubscribeToShard` to boto3 over HTTP/1.1 (finding 9) [R].

### 2.5 Error envelope and status codes

- awsJson1_1 errors: status from the shape (400 for `client`, 500 for `server`, since no `httpError` exists), body `{"__type": "<ShapeName>", "message": "..."}` (the CBOR recording in 2.3 shows the bare shape name and `message`; the JSON form is assumed identical, inferred).
- The per-operation pages list every modeled error with "HTTP Status Code: 400" and `InternalFailureException` with 500 ([PutRecord](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_PutRecord.html)) [D].
- [Common errors](https://docs.aws.amazon.com/kinesis/latest/APIReference/CommonErrors.html): `AccessDeniedException` 403, `ExpiredTokenException` 403, `IncompleteSignature` 403, `InternalFailure` 500, `MalformedHttpRequestException` 400, `NotAuthorized` 401, `OptInRequired` 403, `RequestAbortedException` 400, `RequestEntityTooLargeException` 413, `RequestTimeoutException` 408, `ServiceUnavailable` 503, `ThrottlingException` 400, `UnknownOperationException` 404, `UnrecognizedClientException` 403, `ValidationError` 400 [D]. `AccessDeniedException` is 400 on the operation pages and in the model but 403 here; which AWS sends is UNVERIFIED (KIN-27).

## 3. Identifiers and formats

### 3.1 Stream names, ARNs and the StreamARN alternative

- `StreamName`: length 1 to 128, pattern `^[a-zA-Z0-9_.-]+$` (`com.amazonaws.kinesis#StreamName`) [S]. Scoped to account and region; two streams in one account and region cannot share a name ([create stream](https://docs.aws.amazon.com/streams/latest/dev/kinesis-using-sdk-java-create-stream.html)) [D].
- Stream ARN: `arn:aws:kinesis:<region>:<account>:stream/<name>`. Sources: the Lambda event example `arn:aws:kinesis:us-east-2:123456789012:stream/lambda-stream` ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html)), the provider's acceptance check `"kinesis", "stream/{name}"` (TF `stream_test.go:L53`), and the pattern `^arn:aws.*:kinesis:.*:\d{12}:stream/\S+$` (`com.amazonaws.kinesis#StreamARN`) [S]. The DescribeStream example's `arn:aws:kinesis:us-east-1:111122223333:exampleStreamName` lacks `stream/` and is a doc error (section 11).
- Every stream-scoped input carries optional `StreamName` and `StreamARN`, and the pages say "you must use either the StreamARN or the StreamName parameter, or both" ([DeleteStream](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_DeleteStream.html)) [D]. `UpdateStreamMode`, `RegisterStreamConsumer` and `UpdateMaxRecordSize` take `StreamARN` only (required on the first two) [S]. The errors for neither, for a name and ARN that disagree, and for an ARN naming another region or account are UNVERIFIED (KIN-08).
- `StreamId` (pattern `^[a-z0-9]{20}-[a-z0-9]{3}$`, length 1 to 24) appears on most inputs and on `StreamDescriptionSummary` [S]; the API reference says "Not Implemented. Reserved for future use." on the inputs [D]. Whether AWS returns it in `DescribeStreamSummary` is UNVERIFIED (KIN-03).
- `ResourceARN` (TagResource, UntagResource, ListTagsForResource, resource policies) matches `^arn:aws.*:kinesis:.*:\d{12}:.*(stream|channel)/\S+$`, so it names a stream, a consumer or a channel [S].

### 3.2 Shard ids

- `ShardId`: length 1 to 128, pattern `^[a-zA-Z0-9_.-]+$` [S]. Every example uses `shardId-` and 12 zero-padded digits: `shardId-000000000000` to `shardId-000000000003` ([PutRecords](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_PutRecords.html)), `shardId-000000000006` in the Lambda event [D]. A new stream numbers its shards from 0 (examples). The ids that splits and merges assign (next unused index is the natural reading) are UNVERIFIED (KIN-16).

### 3.3 Hash key space and the even split

- An MD5 hash maps each partition key to a 128-bit integer, and a record goes to the shard whose hash key range contains it ([PutRecord](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_PutRecord.html)) [D]. `HashKey` is a decimal string, pattern `^(0|([1-9]\d{0,38}))$` (`com.amazonaws.kinesis#HashKey`) [S]; the space is `0` to `2^128 - 1 = 340282366920938463463374607431768211455`.
- The documented 3-shard stream ([DescribeStream example](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_DescribeStream.html)):

| Shard | StartingHashKey | EndingHashKey |
|---|---|---|
| `shardId-000000000000` | `0` | `113427455640312821154458202477256070484` |
| `shardId-000000000001` | `113427455640312821154458202477256070485` | `226854911280625642308916404954512140969` |
| `shardId-000000000002` | `226854911280625642308916404954512140970` | `340282366920938463463374607431768211455` |

  `floor(2^128 / 3) = 113427455640312821154458202477256070485`, so the example fits shard `i` starting at `i * floor(2^128 / N)`, ending one before the next start, with the last shard ending at `2^128 - 1` (computed). kinesalite uses the same formula [E] (KL `actions/createStream.js:L33-L45`). For N = 3 the alternative `floor(i * 2^128 / N)` gives the same values; for N = 10 they differ (shard 2 starts at `68056473384187692692674921486353642290` or `...291`). Which formula AWS uses where they differ is UNVERIFIED (KIN-01).
- The ListShards example's ranges (shard 1 `34028236692093846346337460743176821145` to `68056473384187692692674921486353642280`, shard 2 from `...281`) fit neither formula ([ListShards](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListShards.html)); the example looks hand-edited (section 11).
- For four shards both formulas give quarter ranges starting at `0`, `85070591730234615865843651857942052864`, `170141183460469231731687303715884105728`, `255211775190703847597530955573826158592` (computed). The ranges of an on-demand stream's initial four shards are masked in the recording and UNVERIFIED (KIN-02).
- The byte order of the MD5 digest (big-endian, as kinesalite reads the hex digest, KL `db/index.js:L242-L244`) and the encoding of the key (UTF-8) are not documented and UNVERIFIED (KIN-13).

### 3.4 Sequence numbers

- `SequenceNumber`: decimal string, pattern `^(0|([1-9]\d{0,128}))$` [S]. "Sequence numbers increase over time and are specific to a shard within a stream, not across all shards within a stream" (PutRecord); "unique per partition-key within its shard ... The longer the time period between write requests, the larger the sequence numbers become" ([key concepts](https://docs.aws.amazon.com/streams/latest/dev/key-concepts.html)); the PutRecord response page also says "unique across all records in the stream" [D].
- Every recent example is 56 digits starting with `49`: `49590338271490256608559692538361571095921575989136588898` (Lambda event), `49601189658422359378836298521827638475320189012309704722` (Lambda failure record), `49579844037727333356165064238440708846556371693205002258` (ListShards) [D]. Older examples are 38 digits (`21269319989653637946712965403778482177`, PutRecord) [D].
- Structure (inferred): kinesalite's reverse-engineered "version 2" layout, a 47-hex-digit number `2 | shard creation epoch seconds (9 hex) | low hex digit of shard index | per-shard counter (16 hex) | 2 hex | record epoch seconds (9 hex) | shard index (8 hex) | 2` (KL `db/index.js:L129-L221`) [E], decodes the documented record sequence numbers consistently: the Lambda event's first record gives shard index 6 (eventID `shardId-000000000006`) and record time 2018-12-17T22:10:50Z (its `approximateArrivalTimestamp` is `1545084650.987`; the second record decodes 2 s before its timestamp); the failure record gives shard 1 and 2019-11-14T00:38:04Z (its `approximateArrivalOfFirstRecord` is `2019-11-14T00:38:04.835Z`) (computed over the [D] examples). The PutRecords examples do not decode cleanly and look hand-edited. Such numbers lie between `2^185` and `1.5 * 2^185`, so they always have 56 digits, and within a shard lexical and numeric order agree.
- `SequenceNumberForOrdering` "Guarantees strictly increasing sequence numbers, for puts from the same client and to the same partition key ... If this parameter is not set, records are coarsely ordered based on arrival time" (`com.amazonaws.kinesis#PutRecordInput$SequenceNumberForOrdering`) [S]. kinesalite rejects one it cannot parse with `InvalidArgumentException` `ExclusiveMinimumSequenceNumber <n> used in PutRecord on stream <s> under account <a> is invalid.` (KL `actions/putRecord.js:L31-L41`) [E]; AWS's text is UNVERIFIED (KIN-07).
- A shard's `SequenceNumberRange.StartingSequenceNumber` is always present and `EndingSequenceNumber` only on a closed shard (`com.amazonaws.kinesis#SequenceNumberRange`: only the start is required; the DescribeStream example shows an end only on the first shard) [S][D].

### 3.5 Shard iterators

- `ShardIterator`: length 1 to 512 [S]. "A shard iterator expires 5 minutes after it is returned to the requester" ([GetShardIterator](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_GetShardIterator.html)); "Each shard iterator remains valid for 5 minutes, even after it is used" ([developing consumers](https://docs.aws.amazon.com/streams/latest/dev/developing-consumers-with-sdk.html)) [D].
- Examples are base64 and begin `AAAAAAAAAA` (`AAAAAAAAAAETYyAYzd665+8e...`, `AAAAAAAAAAHsW8zCWf91...`) [D]; kinesalite observed a fixed 8-byte prefix `00 00 00 00 00 00 00 01` followed by an encrypted body (KL `db/index.js:L246-L268`) [E]. Clients treat the value as opaque.
- Every GetRecords returns a new `NextShardIterator`, distinct from both earlier iterators (LS `test_kinesis.py:L674-L694`) [R-weak]. An iterator wrapped in literal double quotes (`"\"AAAA...\""`) is accepted (LS `test_kinesis.py:L697-L716`) [R-weak].

### 3.6 Consumer ARNs

- `arn:aws:kinesis:us-east-2:123456789012:stream/lambda-stream/consumer/con1:1540591608`, where the suffix equals the `ConsumerCreationTimestamp` `1540591608.0` in the same `RegisterStreamConsumer` output ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html)) [D]. Pattern `^(arn):aws.*:kinesis:.*:\d{12}:.*stream\/[a-zA-Z0-9_.-]+\/consumer\/[a-zA-Z0-9_.-]+:[0-9]+$` (`com.amazonaws.kinesis#ConsumerARN`) [S].

## 4. Stream lifecycle (control plane)

### 4.1 CreateStream

- Input (`com.amazonaws.kinesis#CreateStreamInput`): `StreamName` (required), `ShardCount` (`PositiveIntegerObject`, at least 1), `StreamModeDetails{StreamMode: PROVISIONED | ON_DEMAND}`, `Tags` (map, 1 to 200 entries, keys 1 to 128, values 0 to 256), `WarmThroughputMiBps` (at least 0), `MaxRecordSizeInKiB` (1024 to 10240), `RecordDistributionStrategy` (`AUTO | USER_PARTITION_KEY`). Output `Unit`. Errors: `InvalidArgumentException`, `LimitExceededException`, `ResourceInUseException`, `ValidationException` [S].
- Modes: "For the data streams with a provisioned mode, you must specify the number of shards"; to create an on-demand stream programmatically, set only the name ([create stream](https://docs.aws.amazon.com/streams/latest/dev/kinesis-using-sdk-java-create-stream.html)) [D]. Omitting both mode and count gave four shards [R] (LS `test_kinesis.snapshot.json:L2-L47`, validated 2022-08-26). New on-demand streams "have 4 MB/s of write and 8 MB/s of read throughput" ([quotas](https://docs.aws.amazon.com/streams/latest/dev/service-sizes-and-limits.html)), four shards of 1 MB/s (inferred). The mode AWS reports for that request is UNVERIFIED (KIN-02). The errors for `ON_DEMAND` with a `ShardCount`, and for `PROVISIONED` without one, are UNVERIFIED (KIN-07).
- `RecordDistributionStrategy`: "only supported for streams that use the on-demand capacity mode. If you do not specify this parameter, the stream uses USER_PARTITION_KEY"; `AUTO` "ignores any partition key and ExplicitHashKey that producers supply" (CreateStream) [D].
- `MaxRecordSizeInKiB`: "The default maximum record size for existing, and newly created streams is set to 1 MiB" ([large records](https://docs.aws.amazon.com/streams/latest/dev/large-records.html)) [D].
- Warm throughput needs the account-level `MinimumThroughputBillingCommitment` ("On-demand Advantage") (CreateStream) [D].
- Tags take effect from `CREATING`, "but you can't make any updates to the tags until the stream is in ACTIVE state"; tagging at creation needs `kinesis:AddTagsToStream`, and "The kinesis:TagResource permission won't work to tag streams on creation" (CreateStream) [D].
- `LimitExceededException` when there would be "more than five streams in the CREATING state" or more shards than the account allows; 5 TPS per account (CreateStream) [D]. Default shard quota: 20,000 per account in us-east-1, us-west-2 and eu-west-1, "1,000 or 6,000" elsewhere; 50 on-demand streams by default ([quotas](https://docs.aws.amazon.com/streams/latest/dev/service-sizes-and-limits.html)) [D].
- Duplicate name: `ResourceInUseException` (inferred from the model's error list); kinesalite's text is `Stream <name> under account <account> already exists.` (KL `actions/createStream.js:L17-L18`) [E]; AWS's text is UNVERIFIED (KIN-05).
- Missing `StreamName` (sent with client validation off) answers 400, with a message LocalStack does not match (LS `test_kinesis.py:L115-L122`, validated 2024-06-10) [R]; the text is UNVERIFIED (KIN-08).

### 4.2 Status transitions

- `StreamStatus`: `CREATING | DELETING | ACTIVE | UPDATING` [S]. CreateStream "immediately returns and sets the stream status to CREATING. After the stream is created, Kinesis Data Streams sets the stream status to ACTIVE. You should perform read and write operations only on an ACTIVE stream" [D]. UpdateShardCount, SplitShard, MergeShards, StartStreamEncryption and UpdateStreamMode set `UPDATING` and return; reads and writes continue while `UPDATING` (their pages; [modes](https://docs.aws.amazon.com/streams/latest/dev/how-do-i-size-a-stream.html)) [D]. MergeShards "If a stream is in the CREATING, UPDATING, or DELETING state, MergeShards returns a ResourceInUseException" [D].
- Waiters: `StreamExists` (DescribeStream, `StreamDescription.StreamStatus == ACTIVE`, `minDelay` 10) and `StreamNotExists` (`ResourceNotFoundException`, `minDelay` 10) (`com.amazonaws.kinesis#DescribeStream` `smithy.waiters#waitable`) [S].
- Timing: undocumented. LocalStack's validated on-demand create plus wait for `ACTIVE` plus six policy calls took 6.43 s of call phase (LS `test_kinesis.validation.json:L32-L40`, 2025-08-05), so `CREATING` lasted under about 6 s there [R]. The length of the window, what DescribeStream returns during it (kinesalite returns an empty `Shards` list until it sets `ACTIVE` [E], KL `actions/createStream.js:L52-L55, L67-L68`), and the error a data call gets on a `CREATING` stream (kinesalite: `ResourceNotFoundException` `Stream <s> under account <a> not found.`, KL `actions/putRecord.js:L14-L17` [E]) are UNVERIFIED (KIN-04).

### 4.3 DeleteStream

- Input: `StreamName`, `StreamARN`, `EnforceConsumerDeletion` [S]. "If this parameter is unset (null) or if you set it to false, and the stream has registered consumers, the call to DeleteStream fails with a ResourceInUseException" (`com.amazonaws.kinesis#DeleteStreamInput$EnforceConsumerDeletion`) [S]. Its message text is UNVERIFIED (KIN-17).
- "If the stream is in the ACTIVE state, you can delete it. After a DeleteStream request, the specified stream is in the DELETING state until Kinesis Data Streams completes the deletion ... Kinesis Data Streams might continue to accept data read and write operations ... on a stream in the DELETING state until the stream deletion is complete." Tags are dissociated ([DeleteStream](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_DeleteStream.html)) [D]. Deleting a `CREATING` stream is UNVERIFIED (KIN-04).

### 4.4 DescribeStream and DescribeStreamSummary

- `DescribeStream` input adds `Limit` (1 to 10000; "The default value is 100. If you specify a value greater than 100, at most 100 results are returned.") and `ExclusiveStartShardId` [S][D]. Output `StreamDescription`: required `StreamName`, `StreamARN`, `StreamStatus`, `Shards`, `HasMoreShards`, `RetentionPeriodHours`, `StreamCreationTimestamp`, `EnhancedMonitoring`; optional `StreamModeDetails`, `EncryptionType`, `KeyId` [S]. "There are no guarantees about the chronological order shards returned"; the API "has been revised" and DescribeStreamSummary plus ListShards are recommended; 10 TPS per account [D].
- `DescribeStreamSummary` output `StreamDescriptionSummary`: required `StreamName`, `StreamARN`, `StreamStatus`, `RetentionPeriodHours`, `StreamCreationTimestamp`, `EnhancedMonitoring`, `OpenShardCount`; optional `StreamId`, `StreamModeDetails`, `EncryptionType`, `KeyId`, `ConsumerCount`, `WarmThroughput{TargetMiBps, CurrentMiBps}`, `MaxRecordSizeInKiB`, `ChannelCount`, `RecordDistributionStrategy` [S]. "DescribeStreamSummary: Returns the MaxRecordSizeInKiB field" ([large records](https://docs.aws.amazon.com/streams/latest/dev/large-records.html)) [D]. 20 TPS per account [D].
- What a fresh stream reports in each optional field (Terraform needs `EncryptionType` and `StreamModeDetails`, section 10.3) is UNVERIFIED (KIN-03). kinesalite reports `EncryptionType: "NONE"` and `EnhancedMonitoring: [{"ShardLevelMetrics": []}]` (KL `actions/createStream.js:L49-L50`) [E].
- `EnhancedMonitoring` is a list of `{ShardLevelMetrics: [...]}`; metric names `IncomingBytes | IncomingRecords | OutgoingBytes | OutgoingRecords | WriteProvisionedThroughputExceeded | ReadProvisionedThroughputExceeded | IteratorAgeMilliseconds | ALL` (`com.amazonaws.kinesis#MetricsName`) [S].

### 4.5 ListStreams

- Input `Limit` (1 to 10000; default 100, at most 100 returned), `ExclusiveStartStreamName`, `NextToken` (1 to 1048576 chars); output `StreamNames` (required), `HasMoreStreams` (required), `NextToken`, `StreamSummaries[{StreamName, StreamARN, StreamStatus, StreamModeDetails, StreamCreationTimestamp}]` [S][D] ([ListStreams](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListStreams.html)). Errors include `ExpiredNextTokenException` [S]. 5 TPS per account [D].
- Order of names, the token format and lifetime are UNVERIFIED (KIN-15).

### 4.6 ListShards

- Input: `StreamName`, `StreamARN`, `NextToken`, `ExclusiveStartShardId`, `MaxResults` (1 to 10000; default 1000, at most 1000 returned), `StreamCreationTimestamp`, `ShardFilter{Type, ShardId, Timestamp}` [S][D] ([ListShards](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListShards.html)).
- Rules [D]: "You cannot specify this parameter if you specify NextToken" for `ExclusiveStartShardId`, `StreamName` and `StreamCreationTimestamp`; "Tokens expire after 300 seconds ... If you specify an expired token in a call to ListShards, you get ExpiredNextTokenException"; "This action does not list expired shards"; 1000 TPS per stream.
- `ShardFilter.Type`: `AFTER_SHARD_ID | AT_TRIM_HORIZON | FROM_TRIM_HORIZON | AT_LATEST | AT_TIMESTAMP | FROM_TIMESTAMP` [S]. "If you specify the AT_TRIM_HORIZON, FROM_TRIM_HORIZON, or AT_LATEST types, you do not need to specify either the ShardId or the Timestamp ... AFTER_SHARD_ID ... must also provide the value for the optional ShardId ... If you specify the AT_TIMESTAMP type, then all shards that were open at the provided timestamp are returned. If you specify the FROM_TIMESTAMP type, then all shards starting from the provided timestamp to TIP are returned" [D]. "You can only specify one filter at a time" [D].
- `StreamCreationTimestamp` distinguishes a re-created stream of the same name [D]; the error when it matches no stream is UNVERIFIED (KIN-15). kinesalite answers `InvalidArgumentException` `NextToken and StreamName cannot be provided together.` and `Either NextToken or StreamName should be provided.` (KL `actions/listShards.js:L7-L9`) [E]; AWS's texts are UNVERIFIED (KIN-15).

### 4.7 Retention period

- `RetentionPeriodHours` has no range trait [S]. Default 24 hours, maximum 8760 (365 days), minimum 24 ([key concepts](https://docs.aws.amazon.com/streams/latest/dev/key-concepts.html)) [D]. Increase: "Must be more than the current retention period"; Decrease: "Must be less than the current retention period" (operation pages) [D]. Increasing "does not make previous, expired data ... accessible" [D].
- Timing: "Kinesis Data Streams stops making records inaccessible at the old retention period within several minutes of increasing the retention period ... almost immediately makes records older than the new retention period inaccessible upon decreasing" ([retention](https://docs.aws.amazon.com/streams/latest/dev/kinesis-extended-retention.html)) [D].
- Equal values: Terraform depends on `Increase` with the current value succeeding (finding 3). kinesalite accepts equal values in both directions (it rejects only strictly shorter or strictly longer, KL `actions/increaseStreamRetentionPeriod.js:L19-L25`, `actions/decreaseStreamRetentionPeriod.js:L19-L25`) [E]. kinesalite's texts: `Minimum allowed retention period is 24 hours. Requested retention period (23 hours) is too short.`; `Requested retention period (25 hours) for stream <s> can not be shorter than existing retention period (48 hours). Use DecreaseRetentionPeriod API.` (and the mirror with `longer` and `IncreaseRetentionPeriod`) (KL lines L13-L25 of both files) [E]. AWS's codes and texts for below 24, above 8760, and the wrong direction, and its answer to an equal `Decrease`, are UNVERIFIED (KIN-06).

### 4.8 Resharding: UpdateShardCount, SplitShard, MergeShards

- All three are "only supported for the data streams with the provisioned capacity mode" (operation docs) and list `ValidationException` [S][D]; the text is UNVERIFIED (KIN-16).
- SplitShard: `ShardToSplit`, `NewStartingHashKey` (both required), "the new hash key might be the average of the beginning and ending hash key, but it can be any hash key value in the range being mapped into the shard"; asynchronous, `UPDATING` then `ACTIVE`; 5 TPS (operation doc) [S][D]. "In the case of a split, the two new shards both have parentShardId equal to the shard ID of the shard that you were processing previously. The value of adjacentParentShardId for both of these shards is null" ([developing consumers](https://docs.aws.amazon.com/streams/latest/dev/developing-consumers-with-sdk.html)) [D].
- MergeShards: `ShardToMerge`, `AdjacentShardToMerge`; "Two shards are considered adjacent if the union of the hash key ranges for the two shards form a contiguous set with no gaps"; the child "has parentShardId equal to shard ID of one of the parent shards and adjacentParentShardId equal to the shard ID of the other parent shard" [S][D].
- Shard states: `OPEN`, then `CLOSED` after a reshard ("data records are no longer added to the shard ... can still be retrieved"), then `EXPIRED` once the retention period passes, when it drops out of shard listings ([after resharding](https://docs.aws.amazon.com/streams/latest/dev/kinesis-using-sdk-java-after-resharding.html)) [D]. "If the shard is closed, GetShardIterator returns a valid iterator for the last sequence number of the shard" (GetShardIterator) [D]. At the end of a closed shard GetRecords returns no `NextShardIterator` and the `ChildShards` list (`com.amazonaws.kinesis#GetRecordsOutput`) [S].
- UpdateShardCount: `TargetShardCount`, `ScalingType` (`UNIFORM_SCALING` only), both required; output `StreamName`, `CurrentShardCount`, `TargetShardCount`, `StreamARN`. "Kinesis Data Streams performs splits or merges on individual shards. This can cause short-lived shards to be created, in addition to the final shards." Default limits: at most ten scalings per rolling 24 hours, not above double or below half the current count, not above 10000, over 10 TPS gives `LimitExceededException` (operation doc) [S][D]. The resulting shard set and lineage are UNVERIFIED (KIN-16).

### 4.9 UpdateStreamMode

- Input `StreamARN` (required), `StreamModeDetails` (required), `WarmThroughputMiBps` [S]. "you can switch between the on-demand and provisioned modes twice within 24 hours ... the status of the stream is set to Updating ... your data stream initially retains whatever shard count it had before the transition" ([modes](https://docs.aws.amazon.com/streams/latest/dev/how-do-i-size-a-stream.html)) [D]. The third-switch error text is UNVERIFIED (KIN-26).
- On-demand streams split a shard when traffic exceeds 500 KB/s per shard, within 15 minutes (same page) [D]. Auto-scaling is load-driven and is not reproduced (13.1).

### 4.10 Tags

- Two APIs. Stream-scoped: `AddTagsToStream` (`Tags` map, required), `RemoveTagsFromStream` (`TagKeys` 1 to 50), `ListTagsForStream` (`ExclusiveStartTagKey`, `Limit` 1 to 50; output `Tags` list and required `HasMoreTags`). Resource-scoped: `TagResource`, `UntagResource`, `ListTagsForResource` (output `Tags` list, no pagination) on any `ResourceARN` [S].
- "You can assign up to 50 tags"; AddTagsToStream overwrites existing keys; RemoveTagsFromStream "If you specify a tag that does not exist, it is ignored"; 5 TPS (operation docs) [D]. Restrictions: 50 tags per resource; keys 1 to 128 Unicode characters, no `aws:` prefix; values 0 to 255 characters; allowed characters "Unicode letters, digits, white space, and ... `_ . / = + - @`" ([tagging](https://docs.aws.amazon.com/streams/latest/dev/tagging.html)) [D]. The model says values up to 256 [S] (section 11).
- Recorded: `ListTagsForStream` after `AddTagsToStream {"foo": "bar"}` returns `Tags[0] == {"Key": "foo", "Value": "bar"}` and `HasMoreTags` false (LS `test_kinesis.snapshot.json:L49-L57`; `test_kinesis.py:L654-L671`, validated 2022-08-25) [R].
- The error code and text for the 51st tag and for an `aws:` key, and ListTagsForStream's page size when `Limit` is omitted, are UNVERIFIED (KIN-07, KIN-15).

### 4.11 Server-side encryption

- `StartStreamEncryption` and `StopStreamEncryption`: `EncryptionType` (`KMS` is the only valid value to start) and `KeyId` both required; `KeyId` may be a key id, key ARN, alias ARN or `alias/<name>`, and "You can also use a master key owned by Kinesis Data Streams by specifying the alias aws/kinesis" ([StartStreamEncryption](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_StartStreamEncryption.html)) [S][D].
- Asynchronous (`UPDATING`, normally seconds); "It can take up to 5 seconds after the stream is in an ACTIVE status before all records written to the stream are encrypted. After you enable encryption, you can verify that encryption is applied by inspecting the API response from PutRecord or PutRecords"; a new key may be applied 25 times per rolling 24 hours (same page) [D].
- Records of an unencrypted stream carry no `EncryptionType`: the AWS-recorded records in LS `test_kinesis.snapshot.json:L58-L88` have only `SequenceNumber`, `ApproximateArrivalTimestamp`, `Data` and `PartitionKey`, and the test skips `$..Records..EncryptionType` because LocalStack emits one (LS `test_kinesis.py:L577-L578`, validated 2022-08-25) [R]. Whether PutRecord and PutRecords responses carry `EncryptionType: NONE`, how `KeyId` is echoed, and when a bad key fails, are UNVERIFIED (KIN-09, KIN-18).

### 4.12 Enhanced monitoring, limits, resource policies, account settings, channels

- `EnableEnhancedMonitoring` / `DisableEnhancedMonitoring`: `ShardLevelMetrics` 1 to 7 names, output `StreamName`, `CurrentShardLevelMetrics`, `DesiredShardLevelMetrics`, `StreamARN` [S]. The metrics go to CloudWatch, which understudy does not feed.
- `DescribeLimits`: `ShardLimit`, `OpenShardCount`, `OnDemandStreamCount`, `OnDemandStreamCountLimit` (required), `ChannelCount`, `ChannelCountLimit`; 1 TPS; "If you update your account limits, the old limits might be returned for a few minutes" [S][D].
- Resource policies: `PutResourcePolicy`, `GetResourcePolicy`, `DeleteResourcePolicy` on `ResourceARN` [S]. Recorded: `GetResourcePolicy` on a stream without a policy answers 200 with `Policy` that the snapshot shows as `{}` (LocalStack's snapshot decodes JSON strings, so the wire value is `"{}"`, inferred); Put then Get returns the stored document (LS `test_kinesis.snapshot.json:L239-L318`, validated 2025-08-05) [R]. Deleting when no policy exists and putting a policy on a missing stream both raise an error whose code is not snapshotted (LS `test_kinesis.py:L210-L220`) [R]; the codes are UNVERIFIED (KIN-25).
- `DescribeAccountSettings`, `UpdateAccountSettings` (On-demand Advantage, `MinimumThroughputBillingCommitment`), `UpdateStreamWarmThroughput`, `UpdateStreamRecordDistributionStrategy`, and `CreateChannel`, `DeleteChannel`, `DescribeChannel`, `ListChannels`, `UpdateChannel` (delivery to S3 and S3 Tables, on-demand only per the [quotas](https://docs.aws.amazon.com/streams/latest/dev/service-sizes-and-limits.html) page) are billing or delivery features with no test-time behavior to model (13.1) [S][D].

## 5. Data plane

### 5.1 PutRecord

- Input (`com.amazonaws.kinesis#PutRecordInput`): `Data` (required blob, 0 to 10485760 bytes), `PartitionKey` (1 to 256, not `@required`), `ExplicitHashKey`, `SequenceNumberForOrdering`, `StreamName`, `StreamARN`, `DryRun`. Output: `ShardId` and `SequenceNumber` (required), `EncryptionType` [S].
- `PartitionKey` is "required" under `USER_PARTITION_KEY` (the default) and "optional and any value you provide is ignored, along with any ExplicitHashKey" under `AUTO` (member documentation) [S]. The error for a missing key on a `USER_PARTITION_KEY` stream is UNVERIFIED (KIN-07).
- "When the data blob (the payload before base64-encoding) is added to the partition key size, the total size must not exceed the maximum record size" (`PutRecordInput$Data`) [S]. Empty data is legal: `Data=""` was written and read back as `b''` (LS `test_kinesis.snapshot.json:L58-L67`, validated 2022-08-25) [R].
- Example response `{"SequenceNumber": "21269319989653637946712965403778482177", "ShardId": "shardId-000000000001"}` without `EncryptionType` (PutRecord) [D].
- "If a PutRecord request cannot be processed because of insufficient provisioned throughput on the shard involved in the request, PutRecord throws ProvisionedThroughputExceededException" (PutRecord) [D].
- `DryRun` "Checks if your request will succeed"; `DryRunOperationException` "The request was rejected because the DryRun parameter was specified" (PutRecord page; `com.amazonaws.kinesis#DryRunOperationException`) [S][D]. Whether a valid dry run answers that error (the EC2 convention) and its text are UNVERIFIED (KIN-07).

### 5.2 PutRecords

- `Records` 1 to 500 entries of `{Data, ExplicitHashKey, PartitionKey}`; output `FailedRecordCount` (`PositiveIntegerObject`, so at least 1 by the model), `Records` (required, 1 to 500 of `{SequenceNumber, ShardId, ErrorCode, ErrorMessage}`), `EncryptionType` [S].
- "Each PutRecords request can support up to 500 records. Each record in the request can be as large as 10 MiB, up to a limit of 10 MiB for the entire request, including partition keys" ([PutRecords](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_PutRecords.html), [quotas](https://docs.aws.amazon.com/streams/latest/dev/service-sizes-and-limits.html)) [D].
- Semantics [D]: "The response Records array always includes the same number of records as the request array ... using natural ordering"; "A single record failure does not stop the processing of subsequent records. As a result, PutRecords doesn't guarantee the ordering of records"; "ErrorCode ... can be one of the following values: ProvisionedThroughputExceededException or InternalFailure".
- Documented partial response (PutRecords example) [D]:

```
{"FailedRecordCount": 2, "Records": [
  {"SequenceNumber": "49543463076548007577105092703039560359975228518395012686", "ShardId": "shardId-000000000000"},
  {"ErrorCode": "ProvisionedThroughputExceededException", "ErrorMessage": "Rate exceeded for shard shardId-000000000001 in stream exampleStreamName under account 111111111111."},
  {"ErrorCode": "InternalFailure", "ErrorMessage": "Internal service failure."}]}
```

  The success example has `"FailedRecordCount": 0`, against the model's minimum of 1 (section 11). Whether AWS sends the zero is UNVERIFIED (KIN-09).

### 5.3 Shard selection

- Explicit hash key, else MD5 of the partition key, then the open shard whose range contains the value (5.1, 3.3) [D]. `ExplicitHashKey` "overrides the partition key to shard mapping" [D]. The model pattern allows 39 digits, above `2^128 - 1`; kinesalite rejects values outside `[0, 2^128-1]` with `InvalidArgumentException` `Invalid ExplicitHashKey. ExplicitHashKey must be in the range: [0, 2^128-1]. Specified value was <v>` (KL `actions/putRecord.js:L22-L28`) [E]; AWS's text is UNVERIFIED (KIN-07).
- Length is counted in "Unicode strings with a maximum length limit of 256 characters" (PutRecord) [D], which matches Smithy's rule that `@length` on a string counts "The number of Unicode scalar values" ([Smithy constraint traits](https://smithy.io/2.0/spec/constraint-traits.html)). AWS accepting 256 multi-byte characters is UNVERIFIED (KIN-13).
- Expected placement on a four-shard even split if the digest is read big-endian over UTF-8 bytes (computed): `a` shard 0, `b` shard 2, `c` shard 1, `d` shard 2, `1` shard 3. Little-endian would give `a` 1, `c` 0, `1` 2. KIN-13 records which.

### 5.4 GetShardIterator

- Input: `ShardId`, `ShardIteratorType` (both required), `StartingSequenceNumber`, `Timestamp`, `StreamName`, `StreamARN`, `DryRun`; output `ShardIterator` (optional in the model) [S].
- Types [D]: `AT_SEQUENCE_NUMBER` and `AFTER_SEQUENCE_NUMBER` use `StartingSequenceNumber`; `AT_TIMESTAMP` uses `Timestamp`, "If a record with this exact time stamp does not exist, the iterator returned is for the next (later) record. If the time stamp is older than the current trim horizon, the iterator returned is for the oldest untrimmed data record (TRIM_HORIZON)"; `TRIM_HORIZON` is "the last untrimmed record in the shard"; `LATEST` is "just after the most recent record in the shard". 5 TPS per account per open shard; "If a GetShardIterator request is made too often, you receive a ProvisionedThroughputExceededException" [D].
- AWS accepted `AT_TIMESTAMP` with `2015-01-01` on a new stream (older than the trim horizon) (LS `test_kinesis.py:L334-L382`, validated 2024-06-21, through SubscribeToShard's `StartingPosition`) and `AT_TIMESTAMP` with the current time over CBOR (L760-L772, validated 2024-07-30) [R].
- Errors for a missing or superfluous sequence number, a sequence number from another shard or stream, a malformed one, a missing or future `Timestamp`, an unknown shard, and a sequence number already trimmed are UNVERIFIED (KIN-12; kinesalite's texts in 6 as hints).

### 5.5 GetRecords

- Input `ShardIterator` (required), `Limit` (1 to 10000), `StreamARN`, `DryRun`; output `Records` (required), `NextShardIterator`, `MillisBehindLatest` (long, at least 0), `ChildShards` [S]. Record: `SequenceNumber`, `Data` (required), `ApproximateArrivalTimestamp`, `PartitionKey`, `EncryptionType` [S].
- Limits [D] ([GetRecords](https://docs.aws.amazon.com/kinesis/latest/APIReference/API_GetRecords.html)): `Limit` "up to 10,000. If you specify a value that is greater than 10,000, GetRecords throws InvalidArgumentException. The default value is 10,000"; "The maximum size of data that GetRecords can return is 10 MiB. If a call returns this amount of data, subsequent calls made within the next 5 seconds throw ProvisionedThroughputExceededException"; 5 TPS per shard; "GetRecords doesn't return any data when it throws an exception."
- Empty results [D]: "If there are no records available in the portion of the shard that the iterator points to, GetRecords returns an empty list. It might take multiple calls to get to a portion of the shard that contains records"; "The GetRecords operation does not block" ([troubleshooting](https://docs.aws.amazon.com/streams/latest/dev/troubleshooting-consumers.html)). An empty shard returns `Records: []` in JSON and CBOR (LS `test_kinesis.py:L552-L575`, validated 2024-07-04) [R].
- Closed shards: "If the shard has been closed, the shard iterator can't return more data and GetRecords returns null in NextShardIterator"; `ChildShards` "returned ... only when the end of the current shard is reached" [S][D]. Open shards always return a `NextShardIterator` ("If the consuming application never calls SplitShard or MergeShards, the shard remains open and the calls to GetRecords never return a NextShardIterator value that is NULL", troubleshooting) [D].
- Example response: `{"MillisBehindLatest": 2100, "NextShardIterator": "AAAAAAAAAAHsW8z...", "Records": [{"Data": "XzxkYXRhPl8w", "PartitionKey": "partitionKey", "ApproximateArrivalTimestamp": 1.441215410867E9, "SequenceNumber": "21269319989652663814458848515492872193"}]}` [D].

### 5.6 Iterator expiry

- 5 minutes from issue, regardless of use (3.5) [D]. `ExpiredIteratorException` "The provided iterator exceeds the maximum age allowed" [S]. kinesalite's text: `Iterator expired. The iterator was created at time <Www Mmm dd HH:MM:SS UTC yyyy> while right now it is <same format> which is further in the future than the tolerated delay of 300000 milliseconds.` (KL `actions/getRecords.js:L41-L46`, and its test `test/getRecords.js:L76-L81`) [E]. AWS's text and the exact boundary are UNVERIFIED (KIN-11).
- The troubleshooting page attributes unexpected expiry to not calling GetRecords "for more than 5 minutes, or because you've performed a restart of your consumer application" ([troubleshooting](https://docs.aws.amazon.com/streams/latest/dev/troubleshooting-consumers.html)) [D]. A consumer recovers by asking for a new iterator after its last processed sequence number (inferred).

### 5.7 Retention on the clock

- "Data records are accessible from the time they are added to the stream to the current retention period. This holds true regardless of any changes to the shards" ([after resharding](https://docs.aws.amazon.com/streams/latest/dev/kinesis-using-sdk-java-after-resharding.html)) [D]. Trim timing on change: 4.7 [D]. Exact trim instant and what `AT_SEQUENCE_NUMBER` of a trimmed record returns are UNVERIFIED (KIN-24, KIN-12).

### 5.8 Record size

- Payload "before base64-encoding is up to 10 MiB" ([quotas](https://docs.aws.amazon.com/streams/latest/dev/service-sizes-and-limits.html)); stream maximum `MaxRecordSizeInKiB` 1024 to 10240, default 1024 ([large records](https://docs.aws.amazon.com/streams/latest/dev/large-records.html)); the limit covers data plus partition key (5.1) [S][D]. The key-concepts page still says "A data blob can be up to 1 MB" (section 11).
- Whether the partition key counts in UTF-8 bytes, whether the boundary is inclusive, the error code and text for an oversized record and for an oversized PutRecords request are UNVERIFIED (KIN-14).

### 5.9 Throughput limits and throttling

- Provisioned shard: writes "up to 1,000 records per second, up to a maximum data write total of 1 MiB per second"; reads "up to five transactions per second, up to a maximum data read total of 2 MiB per second" (CreateStream) [D]. The PutRecord page and the quotas PutRecord row say 10 MiB per second for writes (section 11). "Kinesis is designed to handle intermittent large records (1-10MiB in size) using burst capacity" ([quotas](https://docs.aws.amazon.com/streams/latest/dev/service-sizes-and-limits.html)) [D]. On-demand capacity scales to "double the peak write throughput observed in the previous 30 days" ([modes](https://docs.aws.amazon.com/streams/latest/dev/how-do-i-size-a-stream.html)) [D].
- Control-plane rate limits per account (CreateStream 5 TPS, DescribeStream 10, DescribeStreamSummary 20, ListShards 1000 per stream, and so on, [quotas](https://docs.aws.amazon.com/streams/latest/dev/service-sizes-and-limits.html)) [D]. Only UpdateShardCount's page says what exceeding them returns ("TPS over 10 will trigger the LimitExceededException") [D]; the provider retries `LimitExceededException` when the message contains `simultaneously be in CREATING or DELETING` or `Rate exceeded for stream` (TF `service_package.go:L24-L31`) [C], which implies AWS throttles control calls with that code. UNVERIFIED (KIN-23): whether every control-plane rate limit answers `LimitExceededException` or some answer the common `ThrottlingException` (2.5), the full texts, and the data-plane throttling texts for PutRecord, GetRecords and GetShardIterator.

### 5.10 ApproximateArrivalTimestamp and MillisBehindLatest

- `ApproximateArrivalTimestamp` "is set when a stream successfully receives and stores a record ... The time stamp has millisecond precision. There are no guarantees about the time stamp accuracy, or that the time stamp is always increasing" (GetRecords) [D].
- `MillisBehindLatest`: "The number of milliseconds the GetRecords response is from the tip of the stream, indicating how far behind current time the consumer is. A value of zero indicates that record processing is caught up, and there are no new records to process at this moment" (GetRecords) [D]. A SubscribeToShard event that delivered every record carried `MillisBehindLatest: 0` (LS `test_kinesis.snapshot.json:L146-L188`, validated 2025-08-19) [R]. When records remain, the reference point (now minus the arrival of the next unread record, now minus the last returned, or the tip's arrival minus the last returned) is UNVERIFIED (KIN-10).

## 6. Error catalogue

All shapes have the single member `message` [S]. Status from section 2.5. "Ops" lists where the model binds the error.

| `__type` | Status | Ops [S] | When [D] | Message text | Evidence |
|---|---|---|---|---|---|
| `ResourceNotFoundException` | 400 | 39 ops: all but CreateStream, ListStreams, DescribeLimits, DescribeAccountSettings, UpdateAccountSettings, ListChannels | stream, shard or consumer absent; "If an application attempts to operate on a deleted stream, it receives the exception ResourceNotFoundException" | `Stream <name> under account <account> not found.` | [R] LS `test_kinesis.snapshot.json:L230-L237`. Shard and consumer forms: kinesalite `Shard <id> in stream <s> under account <a> does not exist` and `Could not find shard <id> in stream <s> under account <a>.` [E] (KL `actions/getRecords.js:L60-L66`, `actions/getShardIterator.js:L10-L12`); UNVERIFIED (KIN-05) |
| `ResourceInUseException` | 400 | 29 ops, among them CreateStream, DeleteStream, the reshard, retention, tag, monitoring, encryption and policy writes, RegisterStreamConsumer, ListShards, SubscribeToShard | stream not `ACTIVE`; consumers present on DeleteStream; SubscribeToShard again within 5 s; duplicate CreateStream (inferred) | kinesalite `Stream <s> under account <a> already exists.` [E] | UNVERIFIED (KIN-05, KIN-17) |
| `InvalidArgumentException` | 400 | 43 ops: all but DescribeLimits and DescribeAccountSettings | "A specified parameter exceeds its restrictions, is not supported, or can't be used. For more information, see the returned message." | kinesalite: retention texts (4.7), `Invalid ExplicitHashKey. ...` (5.3), `Invalid ShardIterator.` (KL `actions/getRecords.js:L127`), GetShardIterator combinations such as `Must either specify (1) AT_SEQUENCE_NUMBER or AFTER_SEQUENCE_NUMBER and StartingSequenceNumber or (2) TRIM_HORIZON or LATEST and no StartingSequenceNumber. Request specified <type> and no StartingSequenceNumber.` (KL `actions/getShardIterator.js:L38-L104`) [E] | UNVERIFIED (KIN-06, KIN-07, KIN-12) |
| `LimitExceededException` | 400 | 41 ops: all but PutRecord, PutRecords, GetRecords, GetShardIterator | five streams `CREATING`; shard quota; per-account TPS (inferred except UpdateShardCount, 5.9); sixth consumer `CREATING`; UpdateShardCount over 10 TPS | contains `simultaneously be in CREATING or DELETING` or `Rate exceeded for stream` [C] TF `service_package.go:L26-L27` | full texts UNVERIFIED (KIN-23) |
| `ExpiredIteratorException` | 400 | GetRecords | iterator older than 5 minutes | kinesalite text in 5.6 [E] | UNVERIFIED (KIN-11) |
| `ExpiredNextTokenException` | 400 | ListShards, ListStreams, ListStreamConsumers, ListChannels | ListShards token after 300 s | none | UNVERIFIED (KIN-11) |
| `AccessDeniedException` | 400 (model) or 403 (CommonErrors) | 35 ops | missing permission | none | UNVERIFIED (KIN-27) |
| `ProvisionedThroughputExceededException` | 400 | PutRecord(s), GetRecords, GetShardIterator | rate or size over the shard's capacity | PutRecords entry: `Rate exceeded for shard <shardId> in stream <s> under account <a>.` [D] | operation-level texts UNVERIFIED (KIN-23) |
| `InternalFailureException` | 500 | PutRecord(s), GetRecords, GetShardIterator, SubscribeToShard event | unknown failure | PutRecords entry code `InternalFailure`, message `Internal service failure.` [D] | |
| `ValidationException` | 400 | CreateStream, reshard ops, UpdateStreamMode and the newer ops | provisioned-only API on an on-demand stream | none | UNVERIFIED (KIN-16); its use for constraint failures UNVERIFIED (KIN-07) |
| `KMSAccessDeniedException`, `KMSDisabledException`, `KMSInvalidStateException`, `KMSNotFoundException`, `KMSOptInRequired`, `KMSThrottlingException` | 400 | PutRecord(s), GetRecords, StartStreamEncryption, CreateChannel, SubscribeToShard event | KMS key problems on encrypted streams | none | not modeled by understudy (13.1) |
| `DryRunOperationException` | 400 | PutRecord(s), GetRecords, GetShardIterator, SubscribeToShard | `DryRun` set | none | UNVERIFIED (KIN-07) |

## 7. Enhanced fan-out

### 7.1 Consumers

- `RegisterStreamConsumer`: `StreamARN`, `ConsumerName` (1 to 128, `^[a-zA-Z0-9_.-]+$`), `Tags`; output `Consumer{ConsumerName, ConsumerARN, ConsumerStatus, ConsumerCreationTimestamp}`; status `CREATING | DELETING | ACTIVE` [S]. Limits: 20 consumers per stream (50 under On-demand Advantage); "you cannot have more than 5 consumers in a CREATING status at the same time. Registering a 6th consumer while there are 5 in a CREATING status results in a LimitExceededException"; "A given consumer can only be registered with one stream at a time"; 5 TPS (operation doc; [quotas](https://docs.aws.amazon.com/streams/latest/dev/service-sizes-and-limits.html)) [D]. The register response shows `"ConsumerStatus": "CREATING"` ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html)) [D].
- `DescribeStreamConsumer` (by `ConsumerARN`, or `StreamARN` plus `ConsumerName`) returns `ConsumerDescription` including `StreamARN`; `ListStreamConsumers` on a stream without consumers returns an empty `Consumers` list (LS `test_kinesis.py:L227-L264`, snapshot L126-L145, validated 2022-08-26) [R].
- The duplicate-name error, the `CREATING` duration and the `DELETING` window are UNVERIFIED (KIN-17).

### 7.2 SubscribeToShard

- Input `ConsumerARN`, `ShardId`, `StartingPosition{Type, SequenceNumber, Timestamp}` (types as GetShardIterator), `DryRun`; output `EventStream`, a `@streaming` union of `SubscribeToShardEvent{Records, ContinuationSequenceNumber, MillisBehindLatest, ChildShards}` and the error members `InternalFailureException`, the six KMS errors, `ResourceInUseException`, `ResourceNotFoundException` [S].
- "After the connection is successfully established, Kinesis Data Streams pushes records ... for up to 5 minutes, after which time you need to call SubscribeToShard again"; "If you call SubscribeToShard again with the same ConsumerARN and ShardId within 5 seconds of a successful call, you'll get a ResourceInUseException. If you call SubscribeToShard 5 seconds or more after a successful call, the second call takes over the subscription and the previous connection expires or fails with a ResourceInUseException" (`com.amazonaws.kinesis#SubscribeToShard` documentation) [S].
- Recorded [R]: five records put after subscribing with `TRIM_HORIZON` arrived in one event with `MillisBehindLatest: 0` and records without `EncryptionType` (LS `test_kinesis.snapshot.json:L146-L188`, validated 2025-08-19); `AT_SEQUENCE_NUMBER` with the shard's starting sequence number and `AT_TIMESTAMP` both delivered every record (L89-L125, L193-L229). The Java SDK v2 client also subscribed successfully from a Lambda function (LS `test_kinesis.py:L854-L890`, validated 2024-07-31).
- The HTTP/1.1 framing (status, `Content-Type`, an `initial-response` event, payload encoding of `SubscribeToShardEvent`) is UNVERIFIED (KIN-19).

### 7.3 Scope recommendation: leave SubscribeToShard unbound (501)

1. It needs an `application/vnd.amazon.eventstream` encoder (binary prelude, typed headers, two CRC32s) inside an awsJson1_1 response; understudy has none (no eventstream code under `internal/protocol`).
2. Its main client, KCL on the Java SDK, also needs h2c and CBOR (2.3, 2.4), so EFO is the third layer of a stack whose first two are missing.
3. The behavior is time-shaped (5-minute subscriptions, the 5-second takeover rule, continuation sequence numbers, `ResourceInUseException` delivered as an event), and the framing is unrecorded (KIN-19).
4. Every consumer framework has a polling path: KCL `PollingConfig`, Lambda's standard iterator. A loud 501 tells users which to pick; a half-built stream would fail in ways that look like their bug.

Register, describe, list and deregister are simple control-plane state and should be implemented (13.1), so `aws_kinesis_stream_consumer` and `EnforceConsumerDeletion` behave.

## 8. Lambda Kinesis event

### 8.1 Event shape

From [Using Lambda with Kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html) [D], one record of the example:

```
{
    "kinesis": {
        "kinesisSchemaVersion": "1.0",
        "partitionKey": "1",
        "sequenceNumber": "49590338271490256608559692538361571095921575989136588898",
        "data": "SGVsbG8sIHRoaXMgaXMgYSB0ZXN0Lg==",
        "approximateArrivalTimestamp": 1545084650.987
    },
    "eventSource": "aws:kinesis",
    "eventVersion": "1.0",
    "eventID": "shardId-000000000006:49590338271490256608559692538361571095921575989136588898",
    "eventName": "aws:kinesis:record",
    "invokeIdentityArn": "arn:aws:iam::123456789012:role/lambda-role",
    "awsRegion": "us-east-2",
    "eventSourceARN": "arn:aws:kinesis:us-east-2:123456789012:stream/lambda-stream"
}
```

- The event is `{"Records": [...]}`; `data` is the record's base64; `approximateArrivalTimestamp` is epoch seconds with milliseconds; `eventID` is `<shardId>:<sequenceNumber>`; `invokeIdentityArn` is the execution role; `eventSourceARN` is the stream ARN [D]. "Each batch contains records from a single shard/data stream"; Lambda invokes synchronously [D].
- Byte-level details (field order, timestamp digits, whether `eventSourceARN` names the consumer for an EFO mapping) are UNVERIFIED (KIN-21).

### 8.2 Mapping parameters and failure records

- Kinesis mapping parameters ([parameters](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-parameters.html)) [D]: `BatchSize` default 100, max 10,000; `StartingPosition` required, `AT_TIMESTAMP | TRIM_HORIZON | LATEST`; `MaximumBatchingWindowInSeconds` default 0; `MaximumRecordAgeInSeconds` and `MaximumRetryAttempts` default -1 (infinite); `ParallelizationFactor` 1 to 10; `BisectBatchOnFunctionError`; `FunctionResponseTypes` `ReportBatchItemFailures`; `TumblingWindowInSeconds` 0 to 900; `DestinationConfig` SQS or SNS. Standard iterators poll "each shard ... at a base rate of once per second"; batching windows up to 5 minutes or 6 MB ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html)) [D]. The provider suppresses a `batch_size` diff at 100 for Kinesis (`docs/research/tf-compute.md` section on `batch_size`).
- The SQS or SNS on-failure record has `requestContext{requestId, functionArn, condition, approximateInvokeCount}`, `responseContext{statusCode, executedVersion, functionError}`, `version` `1.0`, `timestamp`, and `KinesisBatchInfo{shardId, startSequenceNumber, endSequenceNumber, approximateArrivalOfFirstRecord, approximateArrivalOfLastRecord, batchSize, streamArn}`; "All of the fields shown in the KinesisBatchInfo property will always be present" ([on-failure destinations](https://docs.aws.amazon.com/lambda/latest/dg/kinesis-on-failure-destination.html)) [D]. The ESM runtime belongs with the shared stream poller and the Lambda notes; this note fixes only what the stream must serve.

## 9. EventBridge Kinesis target

- `Target.KinesisParameters{PartitionKeyPath}`: the object is optional, "If you do not include this parameter, the default is to use the `eventId` as the partition key"; inside it `PartitionKeyPath` is required, at most 256 characters, "The JSON path to be extracted from the event and used as the partition key" ([KinesisParameters](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_KinesisParameters.html), [Target](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_Target.html)) [D].
- "For EC2 instances, Kinesis Data Streams, AWS Step Functions state machines and API Gateway APIs, EventBridge relies on IAM roles that you specify in the RoleARN argument in PutTargets" ([PutTargets](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutTargets.html)) [D]. The PutTargets Kinesis example sets no `RoleArn` and uses the ARN `arn:aws:kinesis:us-east-1:123456789012:function:stream/mystream` (section 11).
- The record's `Data` (the event JSON after `Input`/`InputPath`/`InputTransformer`, inferred from EventBridge's general target semantics), the behavior when `PartitionKeyPath` matches nothing, and whether PutTargets rejects a Kinesis target without `RoleArn` are UNVERIFIED (KIN-22).
- understudy today fails such entries: PutTargets is `partial`, and "a target with ... KinesisParameters ... fails its entry with NotImplemented" (`docs/coverage.md`).

## 10. Terraform aws_kinesis_stream (v6.68.0)

### 10.1 Calls per phase

- Plan: `CustomizeDiff` checks `shard_count` against the mode, then calls `DescribeLimits` and ignores any error ("Explicitly OK if IAM permissions not set (or any other error)") (TF `stream.go:L78-L118`).
- Create (TF `stream.go:L216-L321`): `CreateStream` with `StreamName`, `MaxRecordSizeInKiB` if set, `ShardCount` when the mode is `PROVISIONED`, `StreamModeDetails` if the block is set, `Tags` if any, `WarmThroughputMiBps` if set (L220-L245); wait until `ACTIVE` (L251); set the id to `StreamARN` (L257-L259); `IncreaseStreamRetentionPeriod` with `retention_period` (always, default 24) and wait (L261-L276); `EnableEnhancedMonitoring` if `shard_level_metrics` is non-empty, and wait (L278-L293); `StartStreamEncryption` if `encryption_type = "KMS"`, and wait (L295-L318); then Read.
- Read (TF `stream.go:L323-L374`): `DescribeStreamSummary` by name (L625-L651); tags through `ListTagsForStream` by name, paginated on `HasMoreTags` with `ExclusiveStartTagKey` = last key (TF `tags.go:L84-L108`, `list_pages.go:L35-L50`).
- Update (TF `stream.go:L376-L581`): `UpdateStreamMode` (by ARN), `UpdateShardCount` (`UNIFORM_SCALING`), `IncreaseStreamRetentionPeriod` or `DecreaseStreamRetentionPeriod`, `DisableEnhancedMonitoring` then `EnableEnhancedMonitoring` for the set difference, `StartStreamEncryption` or `StopStreamEncryption` (Stop sends the old type and key), `UpdateMaxRecordSize` (by ARN), `UpdateStreamWarmThroughput` (by ARN); each followed by a wait. Tag changes use `RemoveTagsFromStream` and `AddTagsToStream` in chunks of 10 (TF `tags.go:L110-L156`).
- Delete (TF `stream.go:L583-L608`): `DeleteStream` with `EnforceConsumerDeletion` (default false); `ResourceNotFoundException` counts as done; wait for not-found.
- Import: `DescribeStreamSummary` by name, id set to the ARN (TF `stream.go:L52-L76`).
- Retries: `LimitExceededException` containing `simultaneously be in CREATING or DELETING` or `Rate exceeded for stream` (TF `service_package.go:L24-L31`).

### 10.2 Waiters

- `waitStreamCreated`: pending `CREATING`, target `ACTIVE`; `waitStreamUpdated`: pending `UPDATING`, target `ACTIVE`; `waitStreamDeleted`: pending `DELETING`, target none (not found); all with `Delay: 10 * time.Second`, `MinTimeout: 3 * time.Second`, refreshing with `DescribeStreamSummary` (TF `stream.go:L653-L724`). Timeouts: create 5 minutes, update and delete 120 minutes (L129-L133).
- The 10 s delay is wall-clock and comes before the first refresh, so every create spends at least 20 s waiting (create, then the retention call) and every destroy at least 10 s, whatever understudy does. A state already at the target on the first refresh ends the wait.

### 10.3 Read mapping and plan stability

| Attribute | Schema (TF `stream.go`) | From | understudy must |
|---|---|---|---|
| `arn` | Optional, Computed (L137-L141) | `StreamARN` | return the same ARN every time |
| `encryption_type` | Optional, default `NONE`, case-insensitive diff (L142-L148) | `EncryptionType` (L341) | send `"NONE"` for an unencrypted stream; an omitted field reads as `""` and diffs on every plan |
| `kms_key_id` | Optional (L154-L157) | `KeyId` (L342) | omit `KeyId` when unencrypted; echo it as given when encrypted (KIN-18) |
| `max_record_size_in_kib` | Optional, Computed, 1024 to 10240 (L158-L163) | `MaxRecordSizeInKiB` (L343) | always send it (1024 by default), or an explicit `1024` in config diffs |
| `retention_period` | Optional, default 24, 24 to 8760 (L169-L174) | `RetentionPeriodHours` (L345) | round-trip |
| `shard_count` | Optional (L175-L179) | `OpenShardCount` when `PROVISIONED`, else null (L346-L354) | count open shards only |
| `shard_level_metrics` | Optional set (L180-L187) | union of `EnhancedMonitoring[].ShardLevelMetrics` (L355-L359) | return the stored set |
| `stream_mode_details` | Optional, Computed, missing block suppressed (L188-L203) | `StreamModeDetails` (L360-L366) | send it for `PROVISIONED` too: the basic acceptance test expects `stream_mode_details.#` = 1 and `PROVISIONED` for a config with only `shard_count` (TF `stream_test.go:L37-L79, L863-L870`) |
| `warm_throughput_mib_ps` | Optional, not Computed (L206-L210) | `WarmThroughput.CurrentMiBps` (L367-L371) | omit `WarmThroughput` (or send 0) for streams without warm throughput, or plans diff |
| `tags`, `tags_all` | standard | `ListTagsForStream` | include `CreateStream` tags; implement `HasMoreTags` |

Also needed for apply: `IncreaseStreamRetentionPeriod` with the current value must succeed (finding 3). The acceptance test checks `encryption_type = NONE`, `kms_key_id = ""`, `retention_period = 24`, `shard_count = 2`, `shard_level_metrics.# = 0` (TF `stream_test.go:L54-L64`).

### 10.4 Other resources

- `aws_kinesis_stream_consumer`: `RegisterStreamConsumer`, wait `CREATING` to `ACTIVE` with `DescribeStreamConsumer`, `DeregisterStreamConsumer`, wait `DELETING` to not found (TF `stream_consumer.go:L69-L96, L130-L152, L199-L235`).
- `aws_kinesis_resource_policy`: `PutResourcePolicy`, `GetResourcePolicy`, `DeleteResourcePolicy` (TF `resource_policy.go:L81, L150, L171, L186-L195`).
- `aws_kinesis_account_settings` uses the account-settings operations, unbound here (13.1).

## 11. Conflicts between sources

| Topic | Source A | Source B | Resolution |
|---|---|---|---|
| Increase to the same retention | API ref: "Must be more than the current" | TF calls it with 24 on a 24-hour stream (TF `stream.go:L261-L276`) | accept equal (inferred; KIN-06) |
| Record size | model and API ref: 10 MiB; default 1 MiB | key concepts: "A data blob can be up to 1 MB" | per-stream `MaxRecordSizeInKiB`, default 1024 |
| Shard write rate | CreateStream, PutRecords, key concepts, large records: 1 MiB/s | PutRecord page and quotas PutRecord row: 10 MiB/s | 1 MiB/s sustained; not enforced anyway |
| Shard read rate | CreateStream, key concepts: 5 calls/s, 2 MiB/s | quotas table: "2 MB/sec or 2,000 records/sec" | 5 calls/s, 2 MiB/s; not enforced |
| `FailedRecordCount` | model: `PositiveIntegerObject` (at least 1) | PutRecords success example: `0` | send 0 (doc example; KIN-09) |
| DescribeStream example ARN | model pattern and all other examples: `...:stream/<name>` | `arn:aws:kinesis:us-east-1:111122223333:exampleStreamName` | `stream/` form |
| CreateStream example | text: "creates a stream with three shards" | body has only `StreamName` | the body (an on-demand stream) |
| ListShards example hash keys | DescribeStream example: an even split (both candidate formulas of 3.3 give its values) | ListShards example fits no even split | DescribeStream example (KIN-01) |
| Tag value length | model `TagValue` 0 to 256 | tagging guide: 0 to 255 | model for parsing; KIN-07 for the server's check |
| Tags per stream | docs: 50 | model `TagMap` up to 200 entries | 50 (docs) |
| `ValidationException` | model doc: on-demand misuse | CommonErrors: generic `ValidationError` | both exist; the constraint-failure identity is KIN-07 |
| `AccessDeniedException` status | model and operation pages: 400 | CommonErrors: 403 | model (400) until KIN-27 |
| Sequence number uniqueness | key concepts: "unique per partition-key within its shard" | PutRecord response: "unique across all records in the stream" | unique per stream (the stronger; costs nothing) |
| GetRecords `Limit` over 10,000 | API ref: `InvalidArgumentException` | model `@range(1, 10000)` (generic validation) | `InvalidArgumentException` (KIN-07) |
| Event-stream HTTP version | model `eventStreamHttp: ["h2"]` | AWS served boto3 over HTTP/1.1 [R] | moot while unbound |
| EventBridge Kinesis target | PutTargets text: Kinesis needs `RoleArn` | PutTargets example: no `RoleArn`, ARN `...:function:stream/mystream` | the text (KIN-22) |

## 12. UNVERIFIED register

| Id | Open question | Plausible options | Where it matters |
|---|---|---|---|
| KIN-01 | Even-split formula when 2^128 is not divisible by N | `i * floor(2^128/N)` (kinesalite); `floor(i * 2^128 / N)`; the 3-shard doc example fits both | 3.3; switch `kinesis.hash.even-split` |
| KIN-02 | Mode and hash ranges of a stream created with only a name | `ON_DEMAND` with four quarter ranges; `ON_DEMAND` with other ranges; `PROVISIONED` | 4.1; switch `kinesis.on-demand.initial-shards` |
| KIN-03 | Optional fields on a fresh stream: `EncryptionType`, `EnhancedMonitoring`, `StreamModeDetails`, `MaxRecordSizeInKiB`, `WarmThroughput`, `StreamId`, `ConsumerCount`, `RecordDistributionStrategy` | present with defaults; omitted | 4.4, 10.3 |
| KIN-04 | The `CREATING` window: duration, DescribeStream shape, data-call error, DeleteStream of a `CREATING` stream; the `DELETING` window | `ResourceNotFoundException` or `ResourceInUseException` for data calls | 4.2; switch `kinesis.stateTransitions` (clock mode) |
| KIN-05 | Not-found texts by name over JSON, for shards and for deletes; `ResourceInUseException` text for a duplicate name | kinesalite texts (section 6) | 6 |
| KIN-06 | Retention errors (below 24, above 8760, wrong direction) and equal values in both directions | `InvalidArgumentException` with kinesalite-like texts; `ValidationException` for the range | 4.7; switch `kinesis.retention.equal-value` |
| KIN-07 | Constraint and argument errors: stream name pattern and length, `ShardCount` 0, mode and count mismatch, partition key empty, 257 characters or missing, `ExplicitHashKey` at 2^128, `SequenceNumberForOrdering` garbage, GetRecords `Limit` 10001, PutRecords with 0 or 501 entries, 51 tags, `aws:` tag keys, `DryRun` | `ValidationException` "1 validation error detected: Value ... failed to satisfy constraint ..."; `InvalidArgumentException` | 13.3 validation table |
| KIN-08 | Neither `StreamName` nor `StreamARN`; both disagreeing; ARN of another region or account; `CreateStream` without a name | `InvalidArgumentException`; `ResourceNotFoundException`; `ValidationException` | 3.1 |
| KIN-09 | JSON details: `EncryptionType: NONE` on PutRecord(s) responses, `FailedRecordCount: 0`, timestamp number form, empty GetRecords shape, iterator reuse | present or omitted | 5.1, 5.2, 5.5 |
| KIN-10 | `MillisBehindLatest` reference point | now minus next unread arrival; now minus last returned; tip minus last returned | 5.10; switch `kinesis.get-records.millis-behind-latest` |
| KIN-11 | `ExpiredIteratorException` and `ExpiredNextTokenException` texts; the 300 s boundary | kinesalite text | 5.6 |
| KIN-12 | GetShardIterator and GetRecords argument errors (5.4), and `AT_SEQUENCE_NUMBER` of a trimmed record | error; iterator at the trim horizon | 5.4 |
| KIN-13 | MD5 byte order and key encoding; 256 multi-byte characters accepted | big-endian over UTF-8 (kinesalite); little-endian | 5.3; switch `kinesis.hash.md5-byte-order` |
| KIN-14 | Record size accounting: key in UTF-8 bytes, inclusive boundary, error code and text; PutRecords over 10 MiB | `InvalidArgumentException`; `ValidationException` | 5.8 |
| KIN-15 | ListStreams order and tokens; ListShards token and filter rules and texts; `StreamCreationTimestamp` mismatch; ListTagsForStream default page size; AddTagsToStream tags per call | lexicographic order; page size 10 or 50; 10 tags per call (the provider's chunk size) or 50 | 4.5, 4.6, 4.10 |
| KIN-16 | Split and merge child ids and ranges, parent `EndingSequenceNumber`, `ChildShards` at shard end, UpdateShardCount lineage, the on-demand `ValidationException` text | next index; intermediate shards | 4.8; switches `kinesis.reshard.shard-ids`, `kinesis.reshard.update-shard-count` |
| KIN-17 | Consumer duplicate-name error, `CREATING` and `DELETING` durations, DeleteStream-with-consumers text, `ConsumerCount` | `ResourceInUseException` | 4.3, 7.1 |
| KIN-18 | Encryption: `KeyId` echo form, `EncryptionType` on records and responses, failure of an unknown key (synchronous or after `UPDATING`), StopStreamEncryption with a wrong key | echo as given; resolved ARN | 4.11, 10.3 |
| KIN-19 | SubscribeToShard over HTTP/1.1: status, headers, `initial-response`, event payload encoding, second-subscribe error | eventstream with JSON payloads | 7.2 |
| KIN-20 | CBOR responses: `Content-Type`, integer widths, success timestamp encoding (tag 1 plus integer milliseconds), `__type` form on every error | as Java writes | 2.3 |
| KIN-21 | Lambda Kinesis event bytes: field order, timestamp digits, `eventSourceARN` for an EFO mapping | as documented | 8.1 |
| KIN-22 | EventBridge target: record `Data`, partition key when `PartitionKeyPath` finds nothing, `RoleArn` requirement | entry fails; empty key; event id | 9 |
| KIN-23 | Throttling identity and texts: the control-plane code; data-plane `ProvisionedThroughputExceededException` texts for PutRecord, GetRecords, GetShardIterator | `LimitExceededException` (provider retry) or `ThrottlingException` (CommonErrors); texts contain "Rate exceeded for ..." | 5.9, 13.7 |
| KIN-24 | Exact trim instant after a retention decrease | records older than the new retention vanish at once; within minutes | 5.7; switch `kinesis.retention.trim` |
| KIN-25 | Resource-policy error codes: delete without a policy, put on a missing stream, invalid JSON | `ResourceNotFoundException`; `InvalidArgumentException` | 4.12 |
| KIN-26 | UpdateStreamMode: shard count kept, the third-switch error text | `LimitExceededException` | 4.9 |
| KIN-27 | Status of `AccessDeniedException` | 400 (model); 403 (CommonErrors) | 2.5 |

## 13. Implications for understudy

### 13.1 Operation coverage

Slice 1 is the tracer bullet the shared stream poller needs: create, describe, list, put, iterate, read, retention, tags, delete, plus what Terraform calls. Slice 2 adds resharding, consumers, encryption, policies, record size, mode switches, and h2c with CBOR for Java. Unbound operations answer 501.

| Operation | Slice | Coverage | Exact coverage note |
|---|---|---|---|
| CreateStream | 1 | Partial | `ON_DEMAND streams start with four shards and never scale; WarmThroughputMiBps and RecordDistributionStrategy AUTO are answered with 501 NotImplemented; the account shard and on-demand stream quotas are not enforced` |
| DeleteStream | 1 | Full | |
| DescribeStream | 1 | Full | |
| DescribeStreamSummary | 1 | Full | |
| ListStreams | 1 | Full | |
| ListShards | 1 | Full | |
| IncreaseStreamRetentionPeriod | 1 | Full | |
| DecreaseStreamRetentionPeriod | 1 | Full | |
| PutRecord | 1 | Partial | `per-shard write limits (1 MiB/s, 1,000 records/s) are not enforced and ProvisionedThroughputExceededException comes only from fault injection` |
| PutRecords | 1 | Partial | `per-shard write limits (1 MiB/s, 1,000 records/s) are not enforced; entries fail only through fault injection` |
| GetShardIterator | 1 | Partial | `the five calls per second per shard limit is not enforced` |
| GetRecords | 1 | Partial | `per-shard read limits (5 calls/s, 2 MiB/s and the 5-second wait after a 10 MiB read) are not enforced` |
| AddTagsToStream, RemoveTagsFromStream, ListTagsForStream | 1 | Full | |
| TagResource, UntagResource, ListTagsForResource | 1 | Full | |
| DescribeLimits | 1 | Partial | `ShardLimit is 20000 and OnDemandStreamCountLimit 50 in every region, and neither is enforced` |
| EnableEnhancedMonitoring, DisableEnhancedMonitoring | 1 | StoredOnly | `shard-level metrics are stored and reported on the stream; no metrics are published to CloudWatch` |
| SplitShard, MergeShards | 2 | Full | |
| UpdateShardCount | 2 | Partial | `the stream moves straight to TargetShardCount evenly split shards, children of the open shards they replace; AWS's short-lived intermediate shards and its scaling limits (ten per 24 hours, at most double, at least half) are not reproduced` |
| UpdateStreamMode | 2 | Partial | `the shard count is kept and an ON_DEMAND stream never scales; WarmThroughputMiBps is answered with 501 NotImplemented` |
| UpdateMaxRecordSize | 2 | Full | |
| StartStreamEncryption, StopStreamEncryption | 2 | Partial | `EncryptionType and KeyId are stored and reported on the stream and its records; record data is not encrypted and KMS is not called, so the KMS errors never occur` |
| RegisterStreamConsumer, DescribeStreamConsumer, ListStreamConsumers, DeregisterStreamConsumer | 2 | Full | |
| PutResourcePolicy | 2 | StoredOnly | `the policy is stored and returned by GetResourcePolicy; it is not evaluated` |
| GetResourcePolicy, DeleteResourcePolicy | 2 | Full | |
| SubscribeToShard | none | unbound (501) | section 7.3 |
| UpdateStreamWarmThroughput, UpdateStreamRecordDistributionStrategy, DescribeAccountSettings, UpdateAccountSettings | none | unbound (501) | billing and distribution features without a test-time effect worth faking (4.12) |
| CreateChannel, DeleteChannel, DescribeChannel, ListChannels, UpdateChannel | none | unbound (501) | S3 delivery, outside the roadmap |

That is 35 bound and 10 unbound of 45.

### 13.2 Fidelity switches

| Name | Default | Label | Ground | Record |
|---|---|---|---|---|
| `kinesis.stateTransitions` (configuration, like `lambda.stateTransitions`) | `final` (`CREATING`, `UPDATING`, `DELETING` settle before the call returns); `clock` keeps them for 10 ms of emulator time | aws-doc | 4.2; ARCHITECTURE.md 9 "Provisioning settle" | KIN-04 |
| `kinesis.hash.even-split` | `start(i) = i * floor(2^128 / N)` | inferred | 3.3 (the doc example fits both candidates; kinesalite uses this one) | KIN-01 |
| `kinesis.hash.md5-byte-order` | `big-endian-utf8` | inferred | 3.3, 5.3 | KIN-13 |
| `kinesis.on-demand.initial-shards` | `4` with the even split | third-party-recorded | 4.1 | KIN-02 |
| `kinesis.sequence.layout` | `v2-56-digit` (3.4 layout, per-shard counter) | inferred | 3.4 | KIN-09 and KIN-16 capture live values to compare |
| `kinesis.get-records.empty-batches` | `never` (return available records whenever they exist) | aws-doc | 5.5 (empty batches are allowed, not required) | none |
| `kinesis.get-records.millis-behind-latest` | `now-minus-next-unread-arrival`, 0 when the batch reaches the tip | inferred | 5.10 | KIN-10 |
| `kinesis.retention.trim` | `at-expiry` (a record is gone when arrival plus retention is not after now, on the emulator clock) | aws-doc | 4.7, 5.7 | KIN-24 |
| `kinesis.retention.equal-value` | `accept` for both Increase and Decrease | inferred | 4.7 | KIN-06 |
| `kinesis.reshard.shard-ids` | `next-index` | inferred | 3.2, 4.8 | KIN-16 |
| `kinesis.reshard.update-shard-count` | `direct-even-split` | inferred | 4.8 | KIN-16 |
| `kinesis.creating.data-plane-error` (clock mode only) | `ResourceNotFoundException` with the not-found text | inferred | 4.2 | KIN-04 |

Only `kinesis.hash.even-split`, `kinesis.hash.md5-byte-order`, `kinesis.sequence.layout`, `kinesis.get-records.millis-behind-latest` and `kinesis.retention.equal-value` are built as switches in slice 1, under the per-unit cap of about five new switches (roadmap item 8b). The rest are moot (status transitions are final with no `kinesis.stateTransitions` key, so `kinesis.creating.data-plane-error` never applies; resharding answers 501, so the two `kinesis.reshard.*` rows wait for it), implemented as documented (`kinesis.get-records.empty-batches`, `kinesis.retention.trim`: aws-doc), or fixed recorded behavior (`kinesis.on-demand.initial-shards`, third-party-recorded), with an UNVERIFIED clause in the operation's coverage note where AWS's behavior is unknown.

### 13.3 Limits and validation

Generated validators cover the model's constraints (stream name, `ShardCount >= 1`, `PartitionKey` 1 to 256 scalar values, `Data` up to 10485760, `Records` 1 to 500, `Limit` ranges, tag lengths, hash key and sequence patterns). The validation identity for the overlay (`dev.understudy#validationError`) is UNVERIFIED (KIN-07); use `ValidationException` until recorded, with evidence label `inferred`. Hand-owned checks, each with a `rapid` property test:

| Check | Value | Code | Text until recorded |
|---|---|---|---|
| retention increase or decrease | 24 to 8760; Increase not below current, Decrease not above current | `InvalidArgumentException` | kinesalite forms (4.7), UNVERIFIED (KIN-06) |
| mode and count | `ON_DEMAND` with `ShardCount`; `PROVISIONED` without | `InvalidArgumentException` | UNVERIFIED (KIN-07) |
| record size | `len(Data) + len(UTF-8 PartitionKey) <= MaxRecordSizeInKiB * 1024` | `InvalidArgumentException` | UNVERIFIED (KIN-14) |
| PutRecords request | sum of data plus keys `<= 10 MiB` | `InvalidArgumentException` | UNVERIFIED (KIN-14) |
| `ExplicitHashKey` | `0 <= v <= 2^128 - 1` | `InvalidArgumentException` | kinesalite form, UNVERIFIED (KIN-07) |
| `PartitionKey` | required on `USER_PARTITION_KEY` streams | `InvalidArgumentException` | UNVERIFIED (KIN-07) |
| GetRecords `Limit` | at most 10000 | `InvalidArgumentException` (5.5) | UNVERIFIED text (KIN-07) |
| GetRecords response | at most 10000 records and 10 MiB (5.5); counting partition keys toward the 10 MiB is inferred | none (truncate the batch) | |
| GetShardIterator combinations | 5.4 | `InvalidArgumentException` | kinesalite forms, UNVERIFIED (KIN-12) |
| ListShards exclusivity | `NextToken` with `StreamName`, `StreamCreationTimestamp` or `ExclusiveStartShardId`; neither stream nor token | `InvalidArgumentException` | kinesalite forms, UNVERIFIED (KIN-15) |
| tags | 50 per resource; no `aws:` key | UNVERIFIED (KIN-07) | |
| consumers | 20 per stream | `LimitExceededException` | UNVERIFIED (KIN-17) |
| iterator age | over 300 s of emulator time | `ExpiredIteratorException` | kinesalite form, UNVERIFIED (KIN-11) |
| ListShards token age | over 300 s of emulator time | `ExpiredNextTokenException` | UNVERIFIED (KIN-11) |
| reshard on `ON_DEMAND` | SplitShard, MergeShards, UpdateShardCount | `ValidationException` | UNVERIFIED (KIN-16) |
| stream state | control writes need `ACTIVE`; data calls accepted while `UPDATING` and `DELETING` | `ResourceInUseException` | UNVERIFIED (KIN-05) |

The edge's request cap for Kinesis must exceed a 10 MiB PutRecords after base64 (about 13.4 MiB) plus 500 keys of up to 256 characters escaped in JSON; 16 MiB is a safe floor (inferred). Under CBOR the payload is raw.

### 13.4 Protocol and edge

1. Generate `internal/api/kinesisapi` from the model as `awsJson1_1` with target prefix `Kinesis_20131202` and signing name `kinesis`; the edge claims it by `X-Amz-Target` (ARCHITECTURE.md 4.1 rule 6). The new package needs a row in `internal/ref/deps_test.go`. Ignore `StreamId` on input and never emit it (3.1, KIN-03).
2. Slice 2: add an `application/x-amz-cbor-1.1` body variant to the awsJson codec for this service, built on `internal/protocol/cbor`: maps keyed by member name, blobs as byte strings, timestamps tag 1 with integer epoch milliseconds in both directions, errors as `{"__type", "message"}` maps (2.3). Until it exists, answer a CBOR request with 501 NotImplemented whose CBOR body says to set `CBOR_ENABLED=false` (or `-Daws.cborEnabled=false`), so the Java client can parse it.
3. Slice 2: set `Server.Protocols` with HTTP1 and UnencryptedHTTP2 on the main listener (2.4). It needs no dependency and also serves any other Java async client that assumes h2. Test it with a loopback `KinesisAsyncClient` before claiming KCL support.
4. Errors: 400 for every modeled error, 500 for `InternalFailureException`, bare `__type`, `message` member. Messages that name a stream use `Stream <name> under account <account> not found.` even when the caller sent an ARN (finding 5).

### 13.5 State model and clock

1. Stream: name, ARN, creation timestamp (emulator clock), status, mode, retention, `MaxRecordSizeInKiB`, encryption, metrics set, tags, consumers, policy, and its shard list. Shard: id, parent and adjacent parent, hash range, starting sequence, ending sequence once closed, creation and close instants (for ListShards `AT_TIMESTAMP` and `FROM_TIMESTAMP` filters), and an append-only record log.
2. Record: sequence number, partition key, data blob reference, explicit hash key (not returned), arrival instant (emulator clock, milliseconds). Sequence numbers follow `kinesis.sequence.layout`: strictly increasing per shard, never random, so runs are reproducible (ARCHITECTURE.md 12).
3. Iterators: stateless tokens `base64(00 00 00 00 00 00 00 01 || payload || HMAC)` where the payload holds stream ARN, creation timestamp (so a re-created stream rejects old iterators), shard id, position (`after <seq>` or `at <seq>`) and the issue instant. Reusable until 300 s of emulator time pass. The HMAC key is part of the snapshot, so a restored emulator still accepts iterators issued before the snapshot until they expire. `LATEST` and `AT_TIMESTAMP` resolve to a concrete position when issued (5.4).
4. Retention runs on the emulator clock: reads skip records whose arrival plus retention is not after now; a timer trims logs and expires closed shards whose last record has aged out (EXPIRED shards leave ListShards and DescribeStream, 4.8). An `AT_TIMESTAMP` older than the trim horizon starts at `TRIM_HORIZON` [D].
5. `MillisBehindLatest` per `kinesis.get-records.millis-behind-latest`; `ApproximateArrivalTimestamp` from the emulator clock.
6. GetRecords never blocks (5.5): it returns what exists, up to the limits, and a fresh `NextShardIterator` every time (omitted only at the end of a closed shard, where `ChildShards` is set).
7. Snapshots: streams, shards, record logs (blob store for data), consumers, tags, policies, the iterator key and ListStreams/ListShards token state.

### 13.6 Terraform: apply and an empty second plan

1. CreateStream with `ShardCount` and no mode block makes a `PROVISIONED` stream (finding 4).
2. DescribeStreamSummary on it returns `StreamStatus: ACTIVE` (final mode), `StreamARN` in the `stream/` form, `EncryptionType: "NONE"`, `StreamModeDetails: {"StreamMode": "PROVISIONED"}`, `RetentionPeriodHours`, `OpenShardCount`, `EnhancedMonitoring: [{"ShardLevelMetrics": [...]}]`, `MaxRecordSizeInKiB: 1024`, no `KeyId` and no `WarmThroughput` (10.3).
3. IncreaseStreamRetentionPeriod with the current value succeeds and changes nothing (13.2 `kinesis.retention.equal-value`).
4. ListTagsForStream returns the CreateStream tags with correct `HasMoreTags`.
5. DescribeLimits answers (its errors are ignored, but a 501 would hide the quota check).
6. Destroy: DeleteStream, then DescribeStreamSummary answers `ResourceNotFoundException`.
7. Conformance timing: about 20 s of wall time per stream created and 10 s per stream destroyed from the provider's own waiters (10.2). With `kinesis.stateTransitions: clock` and a frozen clock, a create never leaves `CREATING` and Terraform times out after 5 minutes.

### 13.7 Fault injection

1. The service's throttle identity differs by plane: `LimitExceededException` for control operations (what the provider retries, 5.9; inferred until KIN-23) and `ProvisionedThroughputExceededException` for PutRecord, PutRecords, GetRecords and GetShardIterator. A single per-service `dev.understudy#throttleError` is wrong for half the operations; the overlay needs a per-operation choice or the data-plane identity with control-plane rules naming `LimitExceededException` explicitly.
2. PutRecords needs an entry-level fault: fail selected entries with `ErrorCode: ProvisionedThroughputExceededException`, `ErrorMessage: Rate exceeded for shard <shardId> in stream <name> under account <account>.` or `ErrorCode: InternalFailure`, `ErrorMessage: Internal service failure.` (5.2, [D] texts), set `FailedRecordCount`, and append the others. This is the failure path producers must handle, and it cannot be provoked deterministically otherwise.
3. Throttle texts for the operation-level errors stay labeled UNVERIFIED until KIN-23.

### 13.8 Lambda mappings and EventBridge

1. The stream poller (shared with DynamoDB Streams) reads through `kernel.Invoke` as the execution role: `ListShards`, `GetShardIterator` per shard from the mapping's `StartingPosition`, then GetRecords, woken by PutRecord to that shard rather than by a 1 s timer (a deviation from the documented base rate of once per second, 8.2, that the mapping's coverage note must state), and re-acquiring an iterator with `AFTER_SEQUENCE_NUMBER` on `ExpiredIteratorException`. It follows parent before child shards (4.8) so per-key order holds.
2. The event is built exactly as 8.1: `kinesisSchemaVersion` and `eventVersion` `"1.0"`, `eventID` `<shardId>:<sequenceNumber>`, `invokeIdentityArn` the role, `eventSourceARN` the stream ARN, `awsRegion` the stream's region, `approximateArrivalTimestamp` seconds with milliseconds.
3. EventBridge: add a Kinesis `delivery.Target` that calls PutRecord through `kernel.Invoke` as the target's `RoleArn` with `Data` = the delivered event bytes and `PartitionKey` = the `PartitionKeyPath` result or the event id (9), and drop the NotImplemented entry failure for `KinesisParameters`.

### 13.9 Never fake

- Never fabricate throttling, auto-scaling or resharding that the caller did not ask for; load-driven behavior is a fault rule or a coverage note.
- Never claim encryption: records are stored in the clear, and the coverage note says so; never call KMS for it silently.
- Never accept `SubscribeToShard`, warm throughput, `AUTO` distribution or channels and do nothing; they answer 501.
- Never let a CBOR or HTTP/2 request fail as a generic parse error; it gets a 501 that names the workaround until slice 2.
- A future user guide page must not be named `kinesis.md`: `internal/ref/citations_test.go` rejects a document under `docs/` that shares a research note's file name.

## 14. Recording queue

Agents never run these. A maintainer runs them in the recording shell of `docs/recording-kit.md` sections 1 and 2 (`$P`, `$R`, `$A`, `$REC`, `rec`, `recurl`), in bash, with the session keys exported. Costs use the US East list prices on the [pricing page](https://aws.amazon.com/kinesis/data-streams/pricing/) (provisioned shard-hour USD 0.015, on-demand stream-hour USD 0.040, enhanced fan-out consumer-shard-hour USD 0.015, extended retention shard-hour USD 0.020), rounding every stream up to a full hour.

### 14.0 Helpers and shared streams

Run once per session, after the recording-kit setup:

```sh
export KE=https://kinesis.$R.amazonaws.com/
kin() {  # kin <item>/<step> <Operation> '<json body>': raw awsJson1_1 call, response in $REC/<item>/<step>.http
  SVC=kinesis recurl "$1" -H 'Content-Type: application/x-amz-json-1.1' \
    -H "X-Amz-Target: Kinesis_20131202.$2" --data-binary "$3" "$KE"
}
body() { tail -n 1 "$REC/$1.http"; }   # the one-line JSON body of a kin capture
iter() {  # iter <stream> <shard> [type]: print a shard iterator
  aws kinesis get-shard-iterator --stream-name "$1" --shard-id "$2" \
    --shard-iterator-type "${3:-TRIM_HORIZON}" --query ShardIterator --output text
}
put() {  # put <stream> <partition key> <text>
  aws kinesis put-record --stream-name "$1" --partition-key "$2" \
    --cli-binary-format raw-in-base64-out --data "$3"
}
aws kinesis create-stream --stream-name "$P-k1" --shard-count 1
aws kinesis create-stream --stream-name "$P-k4" --shard-count 4
aws kinesis wait stream-exists --stream-name "$P-k1"
aws kinesis wait stream-exists --stream-name "$P-k4"
export SA1=arn:aws:kinesis:$R:$A:stream/$P-k1 SA4=arn:aws:kinesis:$R:$A:stream/$P-k4
```

Shared teardown, after the last entry: `aws kinesis delete-stream --stream-name "$P-k1" --enforce-consumer-deletion; aws kinesis delete-stream --stream-name "$P-k4" --enforce-consumer-deletion`. Sweep for leftovers: `aws kinesis list-streams --query "StreamNames[?starts_with(@, '$P-')]"`.

Shared cost: 5 shards, USD 0.08 per hour of session.

### 14.1 KIN-01: even-split formula

Settles `kinesis.hash.even-split` (3.3).

```sh
aws kinesis create-stream --stream-name "$P-k10" --shard-count 10
aws kinesis create-stream --stream-name "$P-k7" --shard-count 7
aws kinesis wait stream-exists --stream-name "$P-k10"; aws kinesis wait stream-exists --stream-name "$P-k7"
rec KIN-01/01-k10 aws kinesis list-shards --stream-name "$P-k10"
rec KIN-01/02-k7 aws kinesis list-shards --stream-name "$P-k7"
```

Expected: `shardId-000000000002` of `$P-k10` starts at `68056473384187692692674921486353642290` (`i * floor`) or `...291` (`floor(i*2^128/N)`); `$P-k7` shard 1 starts at `48611766702991209066196372490252601636` either way and shard 2 at `97223533405982418132392744980505203272` or `...273`. Cost USD 0.26, 5 minutes. Teardown: `aws kinesis delete-stream --stream-name "$P-k10"; aws kinesis delete-stream --stream-name "$P-k7"`.

### 14.2 KIN-02: a stream created with only a name

Settles `kinesis.on-demand.initial-shards` (4.1).

```sh
rec KIN-02/01-create aws kinesis create-stream --stream-name "$P-od"
aws kinesis wait stream-exists --stream-name "$P-od"
rec KIN-02/02-summary aws kinesis describe-stream-summary --stream-name "$P-od"
rec KIN-02/03-shards aws kinesis list-shards --stream-name "$P-od"
```

Expected: `StreamMode` `ON_DEMAND`, `OpenShardCount` 4, quarter ranges (3.3). Keep `$P-od` for KIN-03 and KIN-16. Cost USD 0.04, 3 minutes. Teardown: `aws kinesis delete-stream --stream-name "$P-od"` after KIN-16.

### 14.3 KIN-03: optional fields on fresh streams

Settles 4.4 and the Terraform mapping in 10.3.

```sh
kin KIN-03/01-summary-provisioned DescribeStreamSummary "{\"StreamName\":\"$P-k1\"}"
kin KIN-03/02-describe-provisioned DescribeStream "{\"StreamName\":\"$P-k1\"}"
kin KIN-03/03-summary-on-demand DescribeStreamSummary "{\"StreamName\":\"$P-od\"}"
kin KIN-03/04-list-streams ListStreams "{\"ExclusiveStartStreamName\":\"$P-\",\"Limit\":10}"
```

Expected, one line each in `notes.md`: presence and value of `EncryptionType`, `EnhancedMonitoring`, `StreamModeDetails`, `MaxRecordSizeInKiB`, `WarmThroughput`, `StreamId`, `ConsumerCount`, `RecordDistributionStrategy`, `KeyId`, `StreamSummaries`. Cost 0 beyond shared, 2 minutes. Teardown: none.

### 14.4 KIN-04: the CREATING and DELETING windows

Settles `kinesis.stateTransitions` (clock mode) and `kinesis.creating.data-plane-error` (4.2, 4.3).

```sh
aws kinesis create-stream --stream-name "$P-cr" --shard-count 1; start=$(date +%s)
kin KIN-04/01-summary DescribeStreamSummary "{\"StreamName\":\"$P-cr\"}"
kin KIN-04/02-describe DescribeStream "{\"StreamName\":\"$P-cr\"}"
kin KIN-04/03-put PutRecord "{\"StreamName\":\"$P-cr\",\"Data\":\"aGk=\",\"PartitionKey\":\"a\"}"
kin KIN-04/04-iterator GetShardIterator "{\"StreamName\":\"$P-cr\",\"ShardId\":\"shardId-000000000000\",\"ShardIteratorType\":\"LATEST\"}"
aws kinesis wait stream-exists --stream-name "$P-cr"; echo "active after $(( $(date +%s) - start )) s" >> "$REC/KIN-04/notes.md"
aws kinesis create-stream --stream-name "$P-cr2" --shard-count 1
kin KIN-04/05-delete-creating DeleteStream "{\"StreamName\":\"$P-cr2\"}"
kin KIN-04/06-delete DeleteStream "{\"StreamName\":\"$P-cr\"}"
kin KIN-04/07-summary-deleting DescribeStreamSummary "{\"StreamName\":\"$P-cr\"}"
kin KIN-04/08-put-deleting PutRecord "{\"StreamName\":\"$P-cr\",\"Data\":\"aGk=\",\"PartitionKey\":\"a\"}"
```

Cost USD 0.03, 5 minutes. Teardown: `aws kinesis wait stream-exists --stream-name "$P-cr2" && aws kinesis delete-stream --stream-name "$P-cr2"` (if step 05 failed); `$P-cr` deletes itself.

### 14.5 KIN-05: not-found and in-use texts

Settles section 6 rows for `ResourceNotFoundException` and `ResourceInUseException`.

```sh
kin KIN-05/01-summary-name DescribeStreamSummary "{\"StreamName\":\"$P-missing\"}"
kin KIN-05/02-summary-arn DescribeStreamSummary "{\"StreamARN\":\"arn:aws:kinesis:$R:$A:stream/$P-missing\"}"
kin KIN-05/03-put PutRecord "{\"StreamName\":\"$P-missing\",\"Data\":\"aGk=\",\"PartitionKey\":\"a\"}"
kin KIN-05/04-delete DeleteStream "{\"StreamName\":\"$P-missing\"}"
kin KIN-05/05-create-duplicate CreateStream "{\"StreamName\":\"$P-k1\",\"ShardCount\":1}"
kin KIN-05/06-iterator-missing-shard GetShardIterator "{\"StreamName\":\"$P-k1\",\"ShardId\":\"shardId-000000000009\",\"ShardIteratorType\":\"TRIM_HORIZON\"}"
kin KIN-05/07-tags-missing ListTagsForStream "{\"StreamName\":\"$P-missing\"}"
```

Cost 0, 2 minutes. Teardown: none.

### 14.6 KIN-06: retention errors and equal values

Settles `kinesis.retention.equal-value` and the retention rows of 13.3.

```sh
k() { kin "KIN-06/$1" "$2" "{\"StreamName\":\"$P-k1\",\"RetentionPeriodHours\":$3}"; }
k 01-increase-equal IncreaseStreamRetentionPeriod 24
k 02-increase-23 IncreaseStreamRetentionPeriod 23
k 03-increase-8761 IncreaseStreamRetentionPeriod 8761
k 04-increase-48 IncreaseStreamRetentionPeriod 48
kin KIN-06/05-summary-after DescribeStreamSummary "{\"StreamName\":\"$P-k1\"}"
aws kinesis wait stream-exists --stream-name "$P-k1"
k 06-increase-shorter IncreaseStreamRetentionPeriod 25
k 07-decrease-longer DecreaseStreamRetentionPeriod 72
k 08-decrease-equal DecreaseStreamRetentionPeriod 48
k 09-decrease-23 DecreaseStreamRetentionPeriod 23
k 10-decrease-24 DecreaseStreamRetentionPeriod 24
```

Step 05 also shows whether a retention change sets `UPDATING`. Cost USD 0.02 (one extended-retention shard-hour), 3 minutes. Teardown: step 10 restores 24 hours.

### 14.7 KIN-07: constraint and argument errors

Settles the validation identity (`dev.understudy#validationError`) and the KIN-07 rows of 13.3.

```sh
pk257=$(printf 'a%.0s' $(seq 257)); name129=$(printf 'a%.0s' $(seq 129))
recs501=$(python3 -I -c 'import json; print(json.dumps([{"Data": "aGk=", "PartitionKey": "a"}] * 501))')
tags51=$(python3 -I -c 'import json; print(json.dumps({"k%d" % i: "v" for i in range(51)}))')
IT=$(iter "$P-k1" shardId-000000000000)
kin KIN-07/01-name-pattern CreateStream '{"StreamName":"bad name","ShardCount":1}'
kin KIN-07/02-name-129 CreateStream "{\"StreamName\":\"$name129\",\"ShardCount\":1}"
kin KIN-07/03-shardcount-0 CreateStream "{\"StreamName\":\"$P-zero\",\"ShardCount\":0}"
kin KIN-07/04-on-demand-with-count CreateStream "{\"StreamName\":\"$P-odc\",\"ShardCount\":1,\"StreamModeDetails\":{\"StreamMode\":\"ON_DEMAND\"}}"
kin KIN-07/05-provisioned-no-count CreateStream "{\"StreamName\":\"$P-pnc\",\"StreamModeDetails\":{\"StreamMode\":\"PROVISIONED\"}}"
kin KIN-07/06-pk-empty PutRecord "{\"StreamName\":\"$P-k1\",\"Data\":\"aGk=\",\"PartitionKey\":\"\"}"
kin KIN-07/07-pk-257 PutRecord "{\"StreamName\":\"$P-k1\",\"Data\":\"aGk=\",\"PartitionKey\":\"$pk257\"}"
kin KIN-07/08-pk-missing PutRecord "{\"StreamName\":\"$P-k1\",\"Data\":\"aGk=\"}"
kin KIN-07/09-ehk-2-128 PutRecord "{\"StreamName\":\"$P-k1\",\"Data\":\"aGk=\",\"PartitionKey\":\"a\",\"ExplicitHashKey\":\"340282366920938463463374607431768211456\"}"
kin KIN-07/10-seq-ordering PutRecord "{\"StreamName\":\"$P-k1\",\"Data\":\"aGk=\",\"PartitionKey\":\"a\",\"SequenceNumberForOrdering\":\"123\"}"
kin KIN-07/11-limit-10001 GetRecords "{\"ShardIterator\":\"$IT\",\"Limit\":10001}"
kin KIN-07/12-records-0 PutRecords "{\"StreamName\":\"$P-k1\",\"Records\":[]}"
kin KIN-07/13-records-501 PutRecords "{\"StreamName\":\"$P-k1\",\"Records\":$recs501}"
kin KIN-07/14-tags-51 AddTagsToStream "{\"StreamName\":\"$P-k1\",\"Tags\":$tags51}"
kin KIN-07/15-tag-aws-prefix AddTagsToStream "{\"StreamName\":\"$P-k1\",\"Tags\":{\"aws:x\":\"y\"}}"
kin KIN-07/16-dry-run PutRecord "{\"StreamName\":\"$P-k1\",\"Data\":\"aGk=\",\"PartitionKey\":\"a\",\"DryRun\":true}"
```

Cost: any stream steps 03 to 05 create by accident, USD 0.02 each (USD 0.04 if step 04 makes an on-demand stream). 5 minutes. Teardown: `for s in zero odc pnc; do aws kinesis delete-stream --stream-name "$P-$s" 2>/dev/null; done`. Tags that steps 14 and 15 may have applied go with `$P-k1` in the shared teardown. Step 14 fails at the per-call limit first if AddTagsToStream takes at most 10 tags per call (KIN-15 step 10); in that case add 50 tags in five calls and then one more to reach the per-resource limit.

### 14.8 KIN-08: stream identifiers

Settles 3.1.

```sh
OTHER=$([ "$R" = us-east-1 ] && echo us-west-2 || echo us-east-1)
kin KIN-08/01-neither DescribeStreamSummary '{}'
kin KIN-08/02-mismatch DescribeStreamSummary "{\"StreamName\":\"$P-k1\",\"StreamARN\":\"$SA4\"}"
kin KIN-08/03-other-region DescribeStreamSummary "{\"StreamARN\":\"arn:aws:kinesis:$OTHER:$A:stream/$P-k1\"}"
kin KIN-08/04-other-account DescribeStreamSummary "{\"StreamARN\":\"arn:aws:kinesis:$R:111111111111:stream/$P-k1\"}"
kin KIN-08/05-create-no-name CreateStream '{"ShardCount":1}'
```

Cost 0, 2 minutes. Teardown: none.

### 14.9 KIN-09: data-plane JSON details

Settles 5.1, 5.2, 5.5.

```sh
kin KIN-09/01-put PutRecord "{\"StreamName\":\"$P-k1\",\"Data\":\"aGk=\",\"PartitionKey\":\"a\"}"
kin KIN-09/02-put-records PutRecords "{\"StreamName\":\"$P-k1\",\"Records\":[{\"Data\":\"aGk=\",\"PartitionKey\":\"a\"},{\"Data\":\"\",\"PartitionKey\":\"b\"}]}"
IT=$(iter "$P-k1" shardId-000000000000)
kin KIN-09/03-get GetRecords "{\"ShardIterator\":\"$IT\"}"
kin KIN-09/04-get-reused GetRecords "{\"ShardIterator\":\"$IT\"}"
LT=$(iter "$P-k1" shardId-000000000000 LATEST)
kin KIN-09/05-get-empty GetRecords "{\"ShardIterator\":\"$LT\"}"
kin KIN-09/06-get-quoted GetRecords "{\"ShardIterator\":\"\\\"$IT\\\"\"}"
```

Expected: whether 01 and 02 carry `EncryptionType`; whether 02 carries `"FailedRecordCount":0`; the number form of `ApproximateArrivalTimestamp`; that 04 returns the same records as 03; 05's `Records`, `MillisBehindLatest` and `NextShardIterator`. Cost 0, 2 minutes. Teardown: none.

### 14.10 KIN-10: MillisBehindLatest

Settles `kinesis.get-records.millis-behind-latest` (5.10).

```sh
aws kinesis create-stream --stream-name "$P-mbl" --shard-count 1; aws kinesis wait stream-exists --stream-name "$P-mbl"
IT=$(iter "$P-mbl" shardId-000000000000)
put "$P-mbl" a A; sleep 30; put "$P-mbl" a B; sleep 10
kin KIN-10/01-limit-1 GetRecords "{\"ShardIterator\":\"$IT\",\"Limit\":1}"
NEXT=$(body KIN-10/01-limit-1 | jq -r .NextShardIterator)
kin KIN-10/02-rest GetRecords "{\"ShardIterator\":\"$NEXT\"}"
```

Expected for 01: about 40000 (now minus A), 10000 (now minus B, the next unread) or 30000 (B minus A); 02: 0. Cost USD 0.02, 3 minutes. Teardown: `aws kinesis delete-stream --stream-name "$P-mbl"`.

### 14.11 KIN-11: iterator and token expiry

Settles 5.6 and `ExpiredNextTokenException`.

```sh
IT=$(iter "$P-k1" shardId-000000000000)
TOK=$(aws kinesis list-shards --stream-name "$P-k4" --max-results 1 --query NextToken --output text)
sleep 290
kin KIN-11/01-at-290s GetRecords "{\"ShardIterator\":\"$IT\"}"
sleep 20
kin KIN-11/02-at-310s GetRecords "{\"ShardIterator\":\"$IT\"}"
kin KIN-11/03-token-310s ListShards "{\"NextToken\":\"$TOK\"}"
```

Cost 0, 6 minutes. Teardown: none.

### 14.12 KIN-12: GetShardIterator and GetRecords argument errors

Settles 5.4.

```sh
S0=$(aws kinesis put-record --stream-name "$P-k4" --partition-key a --explicit-hash-key 0 --cli-binary-format raw-in-base64-out --data x --query SequenceNumber --output text)
S1=$(aws kinesis put-record --stream-name "$P-k4" --partition-key a --explicit-hash-key 85070591730234615865843651857942052864 --cli-binary-format raw-in-base64-out --data x --query SequenceNumber --output text)
g() { kin "KIN-12/$1" GetShardIterator "$2"; }
g 01-at-seq-missing "{\"StreamName\":\"$P-k4\",\"ShardId\":\"shardId-000000000000\",\"ShardIteratorType\":\"AT_SEQUENCE_NUMBER\"}"
g 02-trim-with-seq "{\"StreamName\":\"$P-k4\",\"ShardId\":\"shardId-000000000000\",\"ShardIteratorType\":\"TRIM_HORIZON\",\"StartingSequenceNumber\":\"$S0\"}"
g 03-other-shard-seq "{\"StreamName\":\"$P-k4\",\"ShardId\":\"shardId-000000000000\",\"ShardIteratorType\":\"AT_SEQUENCE_NUMBER\",\"StartingSequenceNumber\":\"$S1\"}"
g 04-other-stream-seq "{\"StreamName\":\"$P-k1\",\"ShardId\":\"shardId-000000000000\",\"ShardIteratorType\":\"AT_SEQUENCE_NUMBER\",\"StartingSequenceNumber\":\"$S0\"}"
g 05-bad-seq "{\"StreamName\":\"$P-k4\",\"ShardId\":\"shardId-000000000000\",\"ShardIteratorType\":\"AT_SEQUENCE_NUMBER\",\"StartingSequenceNumber\":\"123\"}"
g 06-at-ts-missing "{\"StreamName\":\"$P-k4\",\"ShardId\":\"shardId-000000000000\",\"ShardIteratorType\":\"AT_TIMESTAMP\"}"
g 07-at-ts-future "{\"StreamName\":\"$P-k4\",\"ShardId\":\"shardId-000000000000\",\"ShardIteratorType\":\"AT_TIMESTAMP\",\"Timestamp\":$(( $(date +%s) + 3600 ))}"
g 08-after-latest-seq "{\"StreamName\":\"$P-k4\",\"ShardId\":\"shardId-000000000000\",\"ShardIteratorType\":\"AFTER_SEQUENCE_NUMBER\",\"StartingSequenceNumber\":\"$S0\"}"
kin KIN-12/09-bad-iterator GetRecords '{"ShardIterator":"AAAAAAAAAAEnotarealiterator"}'
```

The trimmed-record case is step 03 of KIN-24. Cost 0, 3 minutes. Teardown: none.

### 14.13 KIN-13: partition-key hashing

Settles `kinesis.hash.md5-byte-order` (3.3, 5.3).

```sh
for k in a b c d 1; do rec "KIN-13/put-$k" aws kinesis put-record --stream-name "$P-k4" --partition-key "$k" --cli-binary-format raw-in-base64-out --data x; done
rec KIN-13/put-eee aws kinesis put-record --stream-name "$P-k4" --partition-key 'ééé' --cli-binary-format raw-in-base64-out --data x
rec KIN-13/ehk-end-0 aws kinesis put-record --stream-name "$P-k4" --partition-key z --explicit-hash-key 85070591730234615865843651857942052863 --cli-binary-format raw-in-base64-out --data x
pk256=$(printf 'é%.0s' $(seq 256))
rec KIN-13/pk-256-multibyte aws kinesis put-record --stream-name "$P-k4" --partition-key "$pk256" --cli-binary-format raw-in-base64-out --data x
```

Expected `ShardId`: `a` 0, `b` 2, `c` 1, `d` 2, `1` 3, `ééé` 3 (big-endian over UTF-8); `ehk-end-0` shard 0; the 256-character key accepted. Cost 0, 2 minutes. Teardown: none.

### 14.14 KIN-14: record and request size

Settles the size rows of 13.3 (5.8).

```sh
mkdir -p "$REC/KIN-14"
mk() {  # mk <file> <data bytes> <partition key>: a PutRecord body
  python3 -I -c 'import base64, json, sys; n = int(sys.argv[2]); print(json.dumps({"StreamName": sys.argv[1], "PartitionKey": sys.argv[3], "Data": base64.b64encode(b"x" * n).decode()}))' "$P-k1" "$2" "$3" > "$1"
}
mk "$REC/KIN-14/at-limit.json" 1048575 a
mk "$REC/KIN-14/over-by-1.json" 1048576 a
mk "$REC/KIN-14/multibyte-key.json" 1048575 é
kin KIN-14/01-at-limit PutRecord @"$REC/KIN-14/at-limit.json"
kin KIN-14/02-over-by-1 PutRecord @"$REC/KIN-14/over-by-1.json"
kin KIN-14/03-multibyte-key PutRecord @"$REC/KIN-14/multibyte-key.json"
python3 -I -c 'import base64, json, sys; d = base64.b64encode(b"x" * 1000000).decode(); print(json.dumps({"StreamName": sys.argv[1], "Records": [{"Data": d, "PartitionKey": str(i)} for i in range(11)]}))' "$P-k4" > "$REC/KIN-14/eleven-mb.json"
kin KIN-14/04-request-over-10mib PutRecords @"$REC/KIN-14/eleven-mb.json"
```

`--data-binary @file` reads the file. Expected: 01 succeeds (1048576 bytes with the key), 02 fails with the size error, 03 succeeds if the key counts in characters (1048576) and fails if it counts in UTF-8 bytes (1048577), 04 fails before any write. Cost about USD 0.01 in PUT units, 4 minutes. Teardown: none.

### 14.15 KIN-15: listing, pagination and filters

Settles 4.5, 4.6, 4.10.

```sh
for s in b a c; do aws kinesis create-stream --stream-name "$P-l-$s" --shard-count 1; done
for s in a b c; do aws kinesis wait stream-exists --stream-name "$P-l-$s"; done
kin KIN-15/01-streams-page ListStreams "{\"ExclusiveStartStreamName\":\"$P-l-\",\"Limit\":2}"
TOK=$(body KIN-15/01-streams-page | jq -r .NextToken)
kin KIN-15/02-streams-next ListStreams "{\"NextToken\":\"$TOK\"}"
kin KIN-15/03-shards-page ListShards "{\"StreamName\":\"$P-k4\",\"MaxResults\":1}"
STOK=$(body KIN-15/03-shards-page | jq -r .NextToken)
kin KIN-15/04-token-and-name ListShards "{\"StreamName\":\"$P-k4\",\"NextToken\":\"$STOK\"}"
kin KIN-15/05-neither ListShards '{}'
kin KIN-15/06-after-shard ListShards "{\"StreamName\":\"$P-k4\",\"ShardFilter\":{\"Type\":\"AFTER_SHARD_ID\",\"ShardId\":\"shardId-000000000001\"}}"
kin KIN-15/07-after-no-id ListShards "{\"StreamName\":\"$P-k4\",\"ShardFilter\":{\"Type\":\"AFTER_SHARD_ID\"}}"
kin KIN-15/08-at-timestamp ListShards "{\"StreamName\":\"$P-k4\",\"ShardFilter\":{\"Type\":\"AT_TIMESTAMP\",\"Timestamp\":$(date +%s)}}"
kin KIN-15/09-created-mismatch ListShards "{\"StreamName\":\"$P-k4\",\"StreamCreationTimestamp\":1000000000}"
tags12=$(python3 -I -c 'import json; print(json.dumps({"k%02d" % i: "v" for i in range(1, 13)}))')
kin KIN-15/10-add-12-tags AddTagsToStream "{\"StreamName\":\"$P-l-a\",\"Tags\":$tags12}"
aws kinesis add-tags-to-stream --stream-name "$P-l-a" --tags $(seq -f 'k%02g=v' 1 10 | paste -sd, -)
aws kinesis add-tags-to-stream --stream-name "$P-l-a" --tags k11=v,k12=v
kin KIN-15/11-tags-default-page ListTagsForStream "{\"StreamName\":\"$P-l-a\"}"
```

Step 10 shows whether AddTagsToStream takes more than 10 tags per call (the provider sends at most 10, TF `tags.go:L118-L120`); step 11 gives the default page size.

Cost USD 0.05, 5 minutes. Teardown: `for s in a b c; do aws kinesis delete-stream --stream-name "$P-l-$s"; done`.

### 14.16 KIN-16: resharding

Settles `kinesis.reshard.shard-ids`, `kinesis.reshard.update-shard-count` and the on-demand `ValidationException` (4.8).

```sh
aws kinesis create-stream --stream-name "$P-rs" --shard-count 2; aws kinesis wait stream-exists --stream-name "$P-rs"
aws kinesis put-record --stream-name "$P-rs" --partition-key a --explicit-hash-key 0 --cli-binary-format raw-in-base64-out --data before-split
IT=$(iter "$P-rs" shardId-000000000000)
rec KIN-16/01-split aws kinesis split-shard --stream-name "$P-rs" --shard-to-split shardId-000000000000 --new-starting-hash-key 85070591730234615865843651857942052864
kin KIN-16/02-summary-updating DescribeStreamSummary "{\"StreamName\":\"$P-rs\"}"
aws kinesis wait stream-exists --stream-name "$P-rs"
rec KIN-16/03-shards-after-split aws kinesis list-shards --stream-name "$P-rs"
kin KIN-16/04-parent-read GetRecords "{\"ShardIterator\":\"$IT\"}"
NEXT=$(body KIN-16/04-parent-read | jq -r .NextShardIterator)
kin KIN-16/05-parent-end GetRecords "{\"ShardIterator\":\"$NEXT\"}"
kin KIN-16/06-closed-iterator GetShardIterator "{\"StreamName\":\"$P-rs\",\"ShardId\":\"shardId-000000000000\",\"ShardIteratorType\":\"LATEST\"}"
```

Read the two child ids from step 03, then:

```sh
rec KIN-16/07-merge aws kinesis merge-shards --stream-name "$P-rs" --shard-to-merge <first child> --adjacent-shard-to-merge <second child>
aws kinesis wait stream-exists --stream-name "$P-rs"
rec KIN-16/08-shards-after-merge aws kinesis list-shards --stream-name "$P-rs"
rec KIN-16/09-update aws kinesis update-shard-count --stream-name "$P-rs" --target-shard-count 4 --scaling-type UNIFORM_SCALING
aws kinesis wait stream-exists --stream-name "$P-rs"
rec KIN-16/10-shards-after-update aws kinesis list-shards --stream-name "$P-rs"
kin KIN-16/11-split-on-demand SplitShard "{\"StreamName\":\"$P-od\",\"ShardToSplit\":\"shardId-000000000000\",\"NewStartingHashKey\":\"1\"}"
```

If step 05 still returns a `NextShardIterator`, repeat GetRecords with it until it is absent and keep the last capture (it carries `ChildShards`). Cost USD 0.15 (up to 10 shard-hours), 15 minutes. Teardown: `aws kinesis delete-stream --stream-name "$P-rs"; aws kinesis delete-stream --stream-name "$P-od"`.

### 14.17 KIN-17: consumers

Settles 4.3 and 7.1.

```sh
aws kinesis create-stream --stream-name "$P-cs" --shard-count 1; aws kinesis wait stream-exists --stream-name "$P-cs"
SC=arn:aws:kinesis:$R:$A:stream/$P-cs
kin KIN-17/01-register RegisterStreamConsumer "{\"StreamARN\":\"$SC\",\"ConsumerName\":\"$P-c1\"}"
kin KIN-17/02-register-duplicate RegisterStreamConsumer "{\"StreamARN\":\"$SC\",\"ConsumerName\":\"$P-c1\"}"
start=$(date +%s); until aws kinesis describe-stream-consumer --stream-arn "$SC" --consumer-name "$P-c1" --query ConsumerDescription.ConsumerStatus --output text | grep -q ACTIVE; do sleep 2; done
echo "consumer active after $(( $(date +%s) - start )) s" >> "$REC/KIN-17/notes.md"
kin KIN-17/03-describe DescribeStreamConsumer "{\"StreamARN\":\"$SC\",\"ConsumerName\":\"$P-c1\"}"
kin KIN-17/04-summary DescribeStreamSummary "{\"StreamName\":\"$P-cs\"}"
kin KIN-17/05-delete-with-consumer DeleteStream "{\"StreamName\":\"$P-cs\"}"
kin KIN-17/06-deregister DeregisterStreamConsumer "{\"StreamARN\":\"$SC\",\"ConsumerName\":\"$P-c1\"}"
kin KIN-17/07-describe-deleting DescribeStreamConsumer "{\"StreamARN\":\"$SC\",\"ConsumerName\":\"$P-c1\"}"
```

Cost USD 0.03, 5 minutes. Teardown: `aws kinesis delete-stream --stream-name "$P-cs" --enforce-consumer-deletion`.

### 14.18 KIN-18: encryption

Settles 4.11 and the `kms_key_id` row of 10.3.

```sh
aws kinesis create-stream --stream-name "$P-enc" --shard-count 1; aws kinesis wait stream-exists --stream-name "$P-enc"
rec KIN-18/01-start aws kinesis start-stream-encryption --stream-name "$P-enc" --encryption-type KMS --key-id alias/aws/kinesis
kin KIN-18/02-summary-updating DescribeStreamSummary "{\"StreamName\":\"$P-enc\"}"
aws kinesis wait stream-exists --stream-name "$P-enc"; sleep 5
kin KIN-18/03-summary DescribeStreamSummary "{\"StreamName\":\"$P-enc\"}"
kin KIN-18/04-put PutRecord "{\"StreamName\":\"$P-enc\",\"Data\":\"aGk=\",\"PartitionKey\":\"a\"}"
kin KIN-18/05-get GetRecords "{\"ShardIterator\":\"$(iter "$P-enc" shardId-000000000000)\"}"
kin KIN-18/06-stop-wrong-key StopStreamEncryption "{\"StreamName\":\"$P-enc\",\"EncryptionType\":\"KMS\",\"KeyId\":\"alias/$P-other\"}"
rec KIN-18/07-stop aws kinesis stop-stream-encryption --stream-name "$P-enc" --encryption-type KMS --key-id alias/aws/kinesis
aws kinesis wait stream-exists --stream-name "$P-enc"
kin KIN-18/08-start-missing-key StartStreamEncryption "{\"StreamName\":\"$P-enc\",\"EncryptionType\":\"KMS\",\"KeyId\":\"alias/$P-missing\"}"
kin KIN-18/09-summary-after-missing-key DescribeStreamSummary "{\"StreamName\":\"$P-enc\"}"
```

Cost USD 0.02 (the AWS-managed key has no monthly charge), 5 minutes. Teardown: `aws kinesis delete-stream --stream-name "$P-enc"`.

### 14.19 KIN-19: SubscribeToShard over HTTP/1.1

Settles 7.2 (framing, for a later decision to bind it).

```sh
aws kinesis create-stream --stream-name "$P-efo" --shard-count 1; aws kinesis wait stream-exists --stream-name "$P-efo"
CA=$(aws kinesis register-stream-consumer --stream-arn "arn:aws:kinesis:$R:$A:stream/$P-efo" --consumer-name "$P-e1" --query Consumer.ConsumerARN --output text)
until aws kinesis describe-stream-consumer --consumer-arn "$CA" --query ConsumerDescription.ConsumerStatus --output text | grep -q ACTIVE; do sleep 2; done
put "$P-efo" a hello
SVC=kinesis recurl KIN-19/01-subscribe --max-time 15 -H 'Content-Type: application/x-amz-json-1.1' \
  -H 'X-Amz-Target: Kinesis_20131202.SubscribeToShard' \
  --data-binary "{\"ConsumerARN\":\"$CA\",\"ShardId\":\"shardId-000000000000\",\"StartingPosition\":{\"Type\":\"TRIM_HORIZON\"}}" "$KE" &
sleep 2
SVC=kinesis recurl KIN-19/02-resubscribe-within-5s --max-time 5 -H 'Content-Type: application/x-amz-json-1.1' \
  -H 'X-Amz-Target: Kinesis_20131202.SubscribeToShard' \
  --data-binary "{\"ConsumerARN\":\"$CA\",\"ShardId\":\"shardId-000000000000\",\"StartingPosition\":{\"Type\":\"LATEST\"}}" "$KE"
wait
for s in 01-subscribe 02-resubscribe-within-5s; do xxd "$REC/KIN-19/$s.http" > "$REC/KIN-19/$s.hex"; done
```

curl exits 28 when `--max-time` ends a stream; that is expected. Step 02 starts 2 s after step 01, inside the 5-second window. Cost USD 0.03, 5 minutes. Teardown: `aws kinesis deregister-stream-consumer --consumer-arn "$CA"; aws kinesis delete-stream --stream-name "$P-efo" --enforce-consumer-deletion`.

### 14.20 KIN-20: CBOR responses

Settles 2.3.

```sh
mkdir -p "$REC/KIN-20"
cbor() {  # cbor <file> <key> <value> ...: a CBOR map of text strings, built with the standard library only
  python3 -I -c '
import sys
def text(s):
    b = s.encode(); n = len(b)
    head = bytes([0x60 | n]) if n < 24 else bytes([0x78, n]) if n < 256 else bytes([0x79]) + n.to_bytes(2, "big")
    return head + b
out, kv = sys.argv[1], sys.argv[2:]
open(out, "wb").write(bytes([0xa0 | len(kv) // 2]) + b"".join(text(x) for x in kv))' "$@"
}
kcbor() {  # kcbor <item>/<step> <Operation> <file>
  SVC=kinesis recurl "$1" -H 'Content-Type: application/x-amz-cbor-1.1' \
    -H "X-Amz-Target: Kinesis_20131202.$2" --data-binary @"$3" "$KE"
}
cbor "$REC/KIN-20/missing.cbor" StreamName "$P-missing"
kcbor KIN-20/01-not-found DescribeStreamSummary "$REC/KIN-20/missing.cbor"
cbor "$REC/KIN-20/k1.cbor" StreamName "$P-k1"
kcbor KIN-20/02-summary DescribeStreamSummary "$REC/KIN-20/k1.cbor"
cbor "$REC/KIN-20/get.cbor" ShardIterator "$(iter "$P-k1" shardId-000000000000)"
kcbor KIN-20/03-get-records GetRecords "$REC/KIN-20/get.cbor"
for s in 01-not-found 02-summary 03-get-records; do xxd "$REC/KIN-20/$s.http" > "$REC/KIN-20/$s.hex"; done
```

Expected: `Content-Type: application/x-amz-cbor-1.1` on all three; timestamps as `c1` followed by an integer head (`1b` and 8 bytes) of epoch milliseconds; `Data` as a byte string (major type 2). Cost 0, 3 minutes. Teardown: none.

### 14.21 KIN-21: the Lambda Kinesis event

Settles 8.1.

```sh
mkdir -p "$REC/KIN-21"
echo '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}' > "$REC/KIN-21/trust.json"
aws iam create-role --role-name "$P-kin-role" --assume-role-policy-document "file://$REC/KIN-21/trust.json"
aws iam attach-role-policy --role-name "$P-kin-role" --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaKinesisExecutionRole
sleep 15
printf 'import json\n\ndef handler(event, context):\n    print(json.dumps(event))\n' > "$REC/KIN-21/index.py"
(cd "$REC/KIN-21" && python3 -I -m zipfile -c fn.zip index.py)
aws lambda create-function --function-name "$P-kin-fn" --runtime python3.12 --handler index.handler \
  --role "arn:aws:iam::$A:role/$P-kin-role" --zip-file "fileb://$REC/KIN-21/fn.zip"
aws lambda wait function-active-v2 --function-name "$P-kin-fn"
rec KIN-21/01-esm aws lambda create-event-source-mapping --function-name "$P-kin-fn" --event-source-arn "$SA1" --starting-position LATEST --batch-size 10
UUID=$(aws lambda list-event-source-mappings --function-name "$P-kin-fn" --query 'EventSourceMappings[0].UUID' --output text)
until aws lambda get-event-source-mapping --uuid "$UUID" --query State --output text | grep -q Enabled; do sleep 5; done
sleep 30; put "$P-k1" pk-1 one; put "$P-k1" pk-2 two; sleep 90
rec KIN-21/02-logs aws logs filter-log-events --log-group-name "/aws/lambda/$P-kin-fn"
```

Cost below USD 0.01, 15 minutes. Teardown: `aws lambda delete-event-source-mapping --uuid "$UUID"; aws lambda delete-function --function-name "$P-kin-fn"; aws logs delete-log-group --log-group-name "/aws/lambda/$P-kin-fn"; aws iam detach-role-policy --role-name "$P-kin-role" --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaKinesisExecutionRole; aws iam delete-role --role-name "$P-kin-role"`.

### 14.22 KIN-22: the EventBridge Kinesis target

Settles section 9.

```sh
mkdir -p "$REC/KIN-22"
aws kinesis create-stream --stream-name "$P-ebk" --shard-count 1; aws kinesis wait stream-exists --stream-name "$P-ebk"
SE=arn:aws:kinesis:$R:$A:stream/$P-ebk
echo '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"sts:AssumeRole"}]}' > "$REC/KIN-22/trust.json"
aws iam create-role --role-name "$P-kin-eb" --assume-role-policy-document "file://$REC/KIN-22/trust.json"
aws iam put-role-policy --role-name "$P-kin-eb" --policy-name put --policy-document "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"kinesis:PutRecord\",\"Resource\":\"$SE\"}]}"
EBR=arn:aws:iam::$A:role/$P-kin-eb; sleep 15
aws events put-rule --name "$P-kin-rule" --event-pattern "{\"source\":[\"$P.kin\"]}"
rec KIN-22/01-targets aws events put-targets --rule "$P-kin-rule" --targets \
  "[{\"Id\":\"path\",\"Arn\":\"$SE\",\"RoleArn\":\"$EBR\",\"KinesisParameters\":{\"PartitionKeyPath\":\"\$.detail.k\"}},{\"Id\":\"default\",\"Arn\":\"$SE\",\"RoleArn\":\"$EBR\"}]"
rec KIN-22/02-target-no-role aws events put-targets --rule "$P-kin-rule" --targets "[{\"Id\":\"norole\",\"Arn\":\"$SE\"}]"
rec KIN-22/03-put-events aws events put-events --entries \
  "[{\"Source\":\"$P.kin\",\"DetailType\":\"t\",\"Detail\":\"{\\\"k\\\":\\\"abc\\\"}\"},{\"Source\":\"$P.kin\",\"DetailType\":\"t\",\"Detail\":\"{}\"}]"
sleep 30
kin KIN-22/04-records GetRecords "{\"ShardIterator\":\"$(iter "$P-ebk" shardId-000000000000)\"}"
```

Expected: four records (two targets times two events, fewer if a target failed); partition keys `abc`, the event ids, and whatever the `path` target did for the event without `k`; `Data` the event JSON. Cost USD 0.02, 8 minutes. Teardown: `aws events remove-targets --rule "$P-kin-rule" --ids path default norole; aws events delete-rule --name "$P-kin-rule"; aws iam delete-role-policy --role-name "$P-kin-eb" --policy-name put; aws iam delete-role --role-name "$P-kin-eb"; aws kinesis delete-stream --stream-name "$P-ebk"`.

### 14.23 KIN-23: throttling texts

Settles 5.9 and 13.7.

```sh
export AWS_MAX_ATTEMPTS=1
for i in $(seq -w 1 40); do kin "KIN-23/ctl-$i" DescribeStream "{\"StreamName\":\"$P-k1\"}" & done; wait
IT=$(iter "$P-k1" shardId-000000000000)
for i in $(seq -w 1 20); do kin "KIN-23/get-$i" GetRecords "{\"ShardIterator\":\"$IT\"}" & done; wait
for i in $(seq -w 1 20); do kin "KIN-23/iter-$i" GetShardIterator "{\"StreamName\":\"$P-k1\",\"ShardId\":\"shardId-000000000000\",\"ShardIteratorType\":\"LATEST\"}" & done; wait
python3 -I -c 'import base64, json, sys; d = base64.b64encode(b"x" * 1024).decode(); print(json.dumps({"StreamName": sys.argv[1], "Records": [{"Data": d, "PartitionKey": "a"}] * 500}))' "$P-k1" > "$REC/KIN-23/burst.json"
for i in 1 2 3 4; do kin "KIN-23/burst-$i" PutRecords @"$REC/KIN-23/burst.json" & done; wait
for i in $(seq -w 1 30); do kin "KIN-23/put-$i" PutRecord @"$REC/KIN-14/at-limit.json" & done; wait
unset AWS_MAX_ATTEMPTS
grep -l -E 'LimitExceeded|ProvisionedThroughputExceeded|Throttling' "$REC"/KIN-23/*.http
```

The last loop reuses the 1 MiB body from KIN-14 (run KIN-14 first). Cost about USD 0.02 in PUT units, 4 minutes. Teardown: none.

### 14.24 KIN-24: trim instant after a retention decrease (optional, 26 hours)

Settles `kinesis.retention.trim` (5.7) and the trimmed-sequence case of KIN-12.

```sh
aws kinesis create-stream --stream-name "$P-ret" --shard-count 1; aws kinesis wait stream-exists --stream-name "$P-ret"
aws kinesis increase-stream-retention-period --stream-name "$P-ret" --retention-period-hours 48
OLD=$(aws kinesis put-record --stream-name "$P-ret" --partition-key a --cli-binary-format raw-in-base64-out --data old --query SequenceNumber --output text)
mkdir -p "$REC/KIN-24"; echo "OLD=$OLD at $(date -u +%FT%TZ)" >> "$REC/KIN-24/notes.md"
```

25 hours later, in a new session (re-run the kit setup and set `OLD` from `notes.md`):

```sh
put "$P-ret" a new
kin KIN-24/01-before-decrease GetRecords "{\"ShardIterator\":\"$(iter "$P-ret" shardId-000000000000)\"}"
aws kinesis decrease-stream-retention-period --stream-name "$P-ret" --retention-period-hours 24
start=$(date +%s)
until ! aws kinesis get-records --shard-iterator "$(iter "$P-ret" shardId-000000000000)" --query 'Records[].Data' --output text | grep -q b2xk; do sleep 10; done
echo "old record gone after $(( $(date +%s) - start )) s" >> "$REC/KIN-24/notes.md"
kin KIN-24/02-after GetRecords "{\"ShardIterator\":\"$(iter "$P-ret" shardId-000000000000)\"}"
kin KIN-24/03-at-trimmed-seq GetShardIterator "{\"StreamName\":\"$P-ret\",\"ShardId\":\"shardId-000000000000\",\"ShardIteratorType\":\"AT_SEQUENCE_NUMBER\",\"StartingSequenceNumber\":\"$OLD\"}"
```

`b2xk` is base64 of `old`. Cost USD 0.91 (26 shard-hours plus 26 extended-retention shard-hours), 26 hours elapsed, 10 minutes of attention. Teardown: `aws kinesis delete-stream --stream-name "$P-ret"`.

### 14.25 KIN-25: resource-policy errors

Settles 4.12.

```sh
POL="{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"arn:aws:iam::$A:root\"},\"Action\":\"kinesis:PutRecord\",\"Resource\":\"$SA1\"}]}"
kin KIN-25/01-get-none GetResourcePolicy "{\"ResourceARN\":\"$SA1\"}"
kin KIN-25/02-delete-none DeleteResourcePolicy "{\"ResourceARN\":\"$SA1\"}"
kin KIN-25/03-put-missing PutResourcePolicy "{\"ResourceARN\":\"arn:aws:kinesis:$R:$A:stream/$P-missing\",\"Policy\":$(jq -Rn --arg p "$POL" '$p')}"
kin KIN-25/04-put-invalid PutResourcePolicy "{\"ResourceARN\":\"$SA1\",\"Policy\":\"not json\"}"
```

Cost 0, 2 minutes. Teardown: `aws kinesis delete-resource-policy --resource-arn "$SA1" 2>/dev/null`.

### 14.26 KIN-26: UpdateStreamMode

Settles 4.9.

```sh
aws kinesis create-stream --stream-name "$P-mode" --shard-count 2; aws kinesis wait stream-exists --stream-name "$P-mode"
SM=arn:aws:kinesis:$R:$A:stream/$P-mode
kin KIN-26/01-to-on-demand UpdateStreamMode "{\"StreamARN\":\"$SM\",\"StreamModeDetails\":{\"StreamMode\":\"ON_DEMAND\"}}"
kin KIN-26/02-summary-updating DescribeStreamSummary "{\"StreamARN\":\"$SM\"}"
aws kinesis wait stream-exists --stream-name "$P-mode"
kin KIN-26/03-summary-on-demand DescribeStreamSummary "{\"StreamARN\":\"$SM\"}"
kin KIN-26/04-to-provisioned UpdateStreamMode "{\"StreamARN\":\"$SM\",\"StreamModeDetails\":{\"StreamMode\":\"PROVISIONED\"}}"
aws kinesis wait stream-exists --stream-name "$P-mode"
kin KIN-26/05-third-switch UpdateStreamMode "{\"StreamARN\":\"$SM\",\"StreamModeDetails\":{\"StreamMode\":\"ON_DEMAND\"}}"
```

Cost USD 0.07, 5 minutes. Teardown: `aws kinesis wait stream-exists --stream-name "$P-mode"; aws kinesis delete-stream --stream-name "$P-mode"`.

### 14.27 KIN-27: AccessDeniedException status

Settles 2.5.

```sh
echo "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"arn:aws:iam::$A:root\"},\"Action\":\"sts:AssumeRole\"}]}" > "$REC/kin-deny-trust.json"
aws iam create-role --role-name "$P-kin-deny" --assume-role-policy-document "file://$REC/kin-deny-trust.json"; sleep 15
(
  eval "$(aws sts assume-role --role-arn "arn:aws:iam::$A:role/$P-kin-deny" --role-session-name kin \
    --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text |
    awk '{print "export AWS_ACCESS_KEY_ID="$1" AWS_SECRET_ACCESS_KEY="$2" AWS_SESSION_TOKEN="$3}')"
  kin KIN-27/01-denied DescribeStreamSummary "{\"StreamName\":\"$P-k1\"}"
)
```

The subshell keeps the recording shell's own keys. Redact before sharing (recording-kit section 2). Cost 0, 3 minutes. Teardown: `aws iam delete-role --role-name "$P-kin-deny"; rm "$REC/kin-deny-trust.json"`.
