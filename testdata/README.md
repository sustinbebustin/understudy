# testdata

Fixtures shared across packages.

- `parity/<svc>/<item>/`: real AWS exchanges for the record backlog items (R01 to R18, KMS-alias), recorded with the interim method of [the recording kit](../docs/human/recording-kit.md) and distilled by `scripts/distill-parity` into one `<step>.json` per step (format below). Each item directory holds the `notes.md` that marks every expected field `matches`, `differs` or `n/a` against understudy as it stood when the captures were compared; the follow-up fix commit closed some of the `differs` lines, and DEFERRED.md ("Recorded differences from AWS") lists the rest. Files with other extensions (`.time`, `.tsv`, `.txt`, `.jsonl`) are side observations the recording scripts wrote, such as timestamps and tallies, kept as written.

`pgregory.net/rapid` failure files do not live here. rapid writes a failing case to `testdata/rapid/<TestName>/*.fail` inside the failing test's own package directory; commit it there as a regression seed and rapid replays it automatically.

Tests never call real AWS. Recordings are made against a throwaway account at the repository owner's request; the replayer that will read them (`internal/parity`) is not built yet.

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
- `headers`: `[name, value]` pairs in the order recorded, duplicates kept. The `Authorization` and `X-Amz-Security-Token` request headers are dropped. botocore logs response headers as a dictionary, so `aws-cli` and `boto3` responses hold one value per name.
- `status` and, where the capture has it, `reason`. `interim_responses` holds curl's `100 Continue` answers.
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
scripts/distill-parity --replace ~/understudy-recordings/usr-rec-20261014
```

It reads the account id from `<recordings>/_scripts/acct` (or `--account-file`), never writes to the recordings, rebuilds `testdata/parity/` from scratch, and fails, naming the steps, when a capture does not parse or a credential or the account id survives. Its tests: `uv run --with pytest pytest scripts/tests`.
