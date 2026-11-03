# testdata

Fixtures shared across packages.

- `parity/<svc>/<item>/`: real AWS exchanges for the record backlog items (R01 to R18, KMS-alias), recorded with the interim method of [the recording kit](../docs/recording-kit.md) and distilled by `scripts/distill-parity` into one `<step>.json` per step (format below). Each item directory holds the `notes.md` that marks every expected field `matches`, `differs` or `n/a` against understudy as it stood when the captures were compared; the follow-up fix commit closed some of the `differs` lines, and [Differences from AWS](../docs/guide/differences.md) lists the rest. Files with other extensions (`.time`, `.tsv`, `.txt`, `.jsonl`, `.hex`) are side observations the recording scripts wrote, such as timestamps, tallies and hex dumps of binary responses, kept as written. A `scenario.json`, written by hand, says how `internal/parity` replays the item ([Replay scenarios](#replay-scenarios)).

`pgregory.net/rapid` failure files do not live here. rapid writes a failing case to `testdata/rapid/<TestName>/*.fail` inside the failing test's own package directory; commit it there as a regression seed and rapid replays it automatically.

Tests never call real AWS. Recordings are made against a throwaway account at the repository owner's request, and `internal/parity` replays them against an in-process understudy.

## Parity fixture format

```json
{
  "step": "02-send",
  "client": "aws-cli",
  "exit": 0,
  "exchanges": [
    {
      "request": {
        "method": "POST",
        "url": "https://sqs.us-east-2.amazonaws.com/",
        "headers": [["X-Amz-Target", "AmazonSQS.SendMessage"], ["Content-Length", "106"]],
        "body": "{\"QueueUrl\": \"...\", \"MessageBody\": \"m1\"}"
      },
      "response": {
        "status": 200,
        "headers": [["x-amzn-RequestId", "23b5af6e-..."], ["Content-Length", "106"]],
        "body": "{\"MD5OfMessageBody\":\"...\",\"MessageId\":\"...\"}"
      }
    }
  ]
}
```

- `client`: what sent the request: `aws-cli` (a `--debug` log), `boto3` (a botocore debug log), `curl`, `http.client` (the R08 and R18 scripts' raw requests) or `socket` (R18's `Expect: 100-continue` step).
- `exit`: the AWS CLI's exit status, or the boto3 script's (0 or 1). Absent for curl, http.client and socket steps, which recorded none.
- `error`: on a non-zero `exit`, the CLI's error text (from its `aws: [ERROR]` line on) or the boto3 exception.
- `stdout`: the client's output, only when no response was recorded (`aws s3 presign`, a client-side validation failure, a log that stops before the response). Otherwise the output restates the response and is dropped.
- `note`: a fact about the capture that the exchange cannot express.
- `exchanges`: every HTTP exchange of the step, in order, retries included. Empty when the client refused the call before sending it. An `aws login` credential refresh in the middle of a step is not an exchange of the step and is dropped.
- `request.url`: absolute, or only the request target where the capture did not record the host (R08's http.client steps). `request` is absent where curl ran without `-v` (R13's function URL steps); the echoed event in the response body shows what the function received.
- `response` is absent where the log holds the request but no response record (two boto3 receives, `sqs/R09/H1-attr-receive-1.json` and `sqs/R09/T17-receive-2.json`); `stdout` then holds the client's output.
- `headers`: `[name, value]` pairs in the order recorded, duplicates kept. The `Authorization` and `X-Amz-Security-Token` request headers are dropped. botocore logs response headers as a dictionary, so `aws-cli` and `boto3` responses hold one value per name.
- `status` and, where the capture has it, `reason`. A `boto3` response has no `status`: the scripts logged only botocore, whose records carry no status line (`s3/R08/02-sdk-head-SHA512.json`). `interim_responses` holds curl's `100 Continue` answers.
- The body, at most one of:
  - `body`: the exact bytes as UTF-8 text.
  - `body_base64`: the exact bytes, which are not UTF-8.
  - `body_omitted`: `{"bytes", "sha256"}` of a body over 64 KiB.
  - `body_unrecorded`: `{"bytes"}`, from `Content-Length` (`null` when unknown), where the capture kept no bytes: curl request bodies, streamed SDK bodies, and http.client bodies the scripts cut short. A Lambda `invoke` response body comes from the payload file the CLI wrote.

  No body key means an empty request body or a response without one (HEAD).

Values that identify the recording account are masked consistently across every file, so cross-references still match: the account id reads `123456789012`; IAM unique ids (`AROA...`, `AIDA...`) that encoded the account are replaced by ids that encode `123456789012`; the role session id becomes `00000000-0000-0000-0000-00000000000N`; the S3 canonical user id, the S3 owner principal id (`AEXAMPLE...`) and the caller's IP address (an RFC 5737 address) get placeholders of the same length; base64 JSON values such as SQS move-task handles are re-encoded with the masked text. Credentials are redacted (`REDACTED`). Unique ids of AWS's own service roles, such as the SNS and S3 `SenderId` roles, are kept.

### Regenerating

The raw captures stay outside the repository, in the recording directory the kit sets up (`~/understudy-recordings/<P>`). Rebuild the fixtures from them with

```sh
scripts/distill-parity ~/understudy-recordings/usr-rec-20261014
```

It reads the account id from `<recordings>/_scripts/acct` (or `--account-file`), never writes to the recordings, and replaces only the steps the recordings hold: each one's fixture is written again, under whichever service held it before, while every other step, every replay scenario (`scenario.json`, written by hand) and every other item's fixtures stay. To retire a step or an item, delete its fixtures by hand. It refuses, writing nothing, when a directory of the recordings names no item id it knows (pass `--skip <dir>` for one that holds no captures) or an item directory holds a file that is neither a capture nor a side observation (`notes.md`, `.time`, `.tsv`, `.txt`, `.jsonl`, `.hex`), such as a helper input. It fails, naming the steps, when a capture does not parse, leaving that step's earlier fixture in place, or when a credential or the account id survives. It copies each `notes.md` from the recordings, so first carry the `Replay` section and the `Replay:` remarks a replay added to an item's `notes.md` into the recording's. Its tests: `uv run --with pytest pytest scripts/tests`.

## Replay scenarios

`parity/<svc>/<item>/scenario.json` tells `internal/parity` how to replay one item against an in-process understudy: which steps run, in which order, what to set up first, which recorded values stand for the ones understudy mints, and which fidelity switches and overlay evidence items a step settles. The checked-in scenarios are `dynamodb/R11` (step 07, DynamoDB Streams' signing scopes), `events/R14` (the `TestEventPattern` cases H1, H8, H9 and H14), `kms/KMS-alias`, `sfn/SFN-01` (Step Functions' error `__type` and `InvalidArn` texts), `sqs/R01` and `sts/R15`. An item without one is not replayed.

```json
{
  "version": 1,
  "item": "KMS-alias",
  "account": "123456789012",
  "region": "us-east-2",
  "client": "aws-cli",
  "setup": [
    {
      "service": "kms",
      "operation": "CreateKey",
      "input": {"Description": "usr-rec-20261014-alias"},
      "bind": [{"path": "body/KeyMetadata/KeyId", "shape": {"kind": "uuid"}, "aws": "157cb509-e842-4095-b0b6-e97188e0a507"}]
    }
  ],
  "steps": [{"fixture": "kms/KMS-alias/01-duplicate.json", "compare": "all"}],
  "settles": [{"overlay": "com.amazonaws.kms#AlreadyExistsException", "step": "01-duplicate", "field": "status"}]
}
```

- `version` is 1; `item` equals the directory name; `account` is the masked account, `123456789012`; `region` is the recording's Region. The replay signs as that account's root (ADR 0012), under both `--sigv4 parse` and `verify`.
- `client`: the client the `record` commands use; each fixture keeps its own.
- `needs`: `["docker"]` for an item whose steps need a Docker daemon. When none serves the run, as under `go test`, every step is skipped.
- `setup`: calls the recording ran outside the kit's `rec` helper, in order, each before the step its `before` names (else before the first step). `service` is the registry's service id (`eventbridge`, not the `events` directory), `operation` the model's name, `input` the operation's input as JSON. A setup `bind` names the recorded value (`aws`) that the call's answer at `path` stands for. A setup call settles nothing; one that fails makes every later step differ. Its input may be made up where the recording did not keep the kit's value (a queue name, a message body, a trust policy); the item's `notes.md` names each made-up input and why, and no `settles` entry may sit on a step whose result depends on one.
- `steps`: each `fixture`, listed in the order the steps were recorded (a test checks each first request's `X-Amz-Date`) and run in that order, never in file-name order. `compare` is `all`, or `final` when the earlier exchanges are client retries. `advance` (a Go duration such as `6s`) moves the emulator clock before the step, whether or not the step can be sent, since the later steps depend on the time it stands for: the kit's `sleep`, or a wait the frozen clock does not pass (a long poll's), inferred from the recorded `X-Amz-Date` gaps and named in `notes.md`. A step `bind` reads the value at `path` from the recorded response and from understudy's: a JSON string, XML text or attribute value, or a header, never a JSON number, which the substitution cannot rewrite. Later requests carry understudy's value, and understudy's answers are compared with the recorded one in its place.
- A bind's `shape` is checked on both sides first: `uuid`; `id` (`alphabet` hex, base32, base64, alnum or digits, `min`, `max`, optional `prefix`); `arn` (`service`, `resource` type, optional `name` shape); `url` (`path` template such as `/{account}/{name}`). A mismatch fails the step. The shape checks each side's own form, not that the two agree: the SQS receipt handle binds admit understudy's 36 characters and AWS's 412, and the bound and receipt-handle normalizers hide the value, so a matching receive says nothing about handle length ([Differences from AWS](../docs/guide/differences.md) lists it).
- A recorded step the replayer cannot reproduce is left out of its scenario and named in the item's `notes.md`: `sts/R15/06-get-caller-identity-assumed.json` was signed with the session credentials an earlier step returned, and the replay signs only as the account's root.
- `waivers`: `{step, field, reason}` hides one compared field, and everything under it, from a step's comparison. None is in use.
- `settles`: `{switch, step, field}` or `{overlay, covers, step, field}` (`covers`: which of the overlay entry's evidence items, by trait; required when the shape has more than one evidence item, since a claim without it names them all) claims that the step's `field` decides that switch or overlay entry. A switch is claimed by one step at most, the one its `Settles` names in code (`fidelity.SettledBy`); a test holds the two together.
- `record`: only in a scenario whose steps are not recorded yet: the AWS CLI argv of each, `{step, argv}` for a `rec` capture and `{argv, capture}` for a command run outside `rec`, whose stdout is kept in the shell variable `capture` names (optional). `scripts/parity-record` prints them for the recording shell ([the recording kit](../docs/recording-kit.md)). A step whose fixture does not exist yet, and that a `rec` entry names, is `pending`.

A comparison path is `status`, `error/code`, `error/message`, `header/<lower-case name>`, or `body/` followed by JSON pointer tokens (`body/Messages/0/ReceiptHandle`) or XML element names from the root (`body/ErrorResponse/Error/Code`, `member[1]` for the second `member`, `@xmlns` for an attribute). The status, the error code and message, Content-Type and the `x-amz-*` and `x-amzn-*` response headers other than ids, dates and digests, and the whole body are compared: JSON with member order, XML as a tree. Only a waiver, or a normalizer of the closed set in `internal/normalize` (request and host ids, timestamps, the account and the masked identity values above, credentials, receipt handles, bound values), hides a difference. The loader refuses an unknown, repeated or missing member, naming its path (`steps[0].bind[0].shape.min`). A checked-in file is in the canonical form `Scenario.Encode` writes (members in the order above, two-space indentation, unlike the compacted example); a test prints that form when a file differs from it.

`go test -race ./internal/parity` replays every scenario and records, per step, `match`, `differs` (with the paths that differ), `reconstructed` (a request or response the capture did not record whole: sent at most, never compared), `skipped` (`docker`, `rest-routing` for S3's and Lambda's REST requests, which the replayer does not route yet, or `no-exchange` when the client refused the call before sending it) or `pending`, with the operation understudy routed the step to. A claim is `settled` only when its step matches and fails once the recorded value at `field` is changed (so no waiver or normalizer hides it), `contradicted` when the step differs, `insensitive` when a waiver or normalizer hides the field, and `open` when the step was not compared. The outcomes are a ratchet in `internal/parity/baseline.json`: the test fails on any change, naming the step. Record an intended change with `just parity-baseline`, then `just coverage-check`, which carries the results into the evidence column of `docs/coverage.md`. A step that newly differs from its recording is a finding: fix understudy or the research, never the scenario. A `differs` kept in the baseline is a known difference, and stays visible: a fidelity switch, an operation's coverage note, or a line in the item's `notes.md` and [Differences from AWS](../docs/guide/differences.md).
