# SSM-22: KMS key references and failures (ssm-parameters.md 13.23)

- Date: 2026-11-03, 03:08Z
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux x86_64
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Step | Expected | Result |
|---|---|---|
| 01 KeyId echo (5.4, `ssm.kms.key-id-echo`) | stored key reference echoed verbatim | matches: key id -> key id, key ARN -> key ARN, alias ARN -> alias ARN, no KeyId -> `alias/aws/ssm` |
| 02 history KeyId | as 01 | matches: key id echoed |
| 03 direct `kms decrypt` of a default-key ciphertext | recipe: `Plaintext` `cw==` | differs: 400 `AccessDeniedException`, "... is not authorized to perform: kms:Decrypt on resource: <alias/aws/ssm key ARN> because no resource-based policy allows the kms:Decrypt action". Confirms 5.6's amended inference (managed key policy requires `kms:ViaService`); the recipe's expectation is stale |
| 04 missing alias | `InvalidKeyId` with KMS's message | matches: 400 `{"__type":"InvalidKeyId","message":"Alias arn:aws:kms:<r>:<acct>:alias/<p>-none is not found. (Service: AWSKMS; Status Code: 400; Error Code: NotFoundException; Request ID: <id>; Proxy: null)"}` |
| 05 disabled key, put | `InvalidKeyId` | matches: `"<key ARN> is disabled. (Service: AWSKMS; Status Code: 400; Error Code: DisabledException; Request ID: <id>; Proxy: null)"` |
| 06 disabled key, get with decryption | `InvalidKeyId` | matches: same text as 05 |
| 07 read with decryption, caller without `kms:Decrypt` (5.5) | understudy: `InvalidKeyId` carrying KMS's message | differs: 400 `{"__type":"AccessDeniedException","Message":"User: arn:aws:sts::<acct>:assumed-role/<p>-ssm-k7/ssm is not authorized to perform: kms:Decrypt on resource: <key ARN> because no identity-based policy allows the kms:Decrypt action (Service: AWSKMS; Status Code: 400; Error Code: AccessDeniedException; Request ID: <id>; Proxy: null)"}`. Note capital `Message` |
| 08a/08b default key, caller with only ssm:Put/GetParameter (5.6) | both succeed | matches |

Replay (scenario.json): steps 01, 02, 04, 05 and 06. The setup recreates the key (bound to the recorded key id), the alias and the four parameters, and disables the key before 05. Step 02's ciphertext is waived (opaque, fresh per encryption). Step 03 is a direct KMS call outside SSM with no fixture; steps 07, 08a and 08b were signed by purpose-made roles whose policies the replay does not hold, so they stay evidence only. Steps 04-06 match: understudy appends the KMS client suffix `(Service: AWSKMS; ...; Proxy: null)` with a request id of its own, which the request-id normalizer masks inside the suffix.
