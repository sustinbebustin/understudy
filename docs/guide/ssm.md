# SSM Parameter Store

understudy serves the thirteen Parameter Store operations of SSM: `PutParameter`, `GetParameter`, `GetParameters`, `GetParametersByPath`, `DescribeParameters`, `GetParameterHistory`, `LabelParameterVersion`, `UnlabelParameterVersion`, `DeleteParameter`, `DeleteParameters` and the three tag operations. The other 139 SSM operations, such as Run Command, Session Manager and the service settings, answer 501. `understudy coverage --service ssm` lists each operation with the gaps it has.

The commands below were run against a build of this repository with `eval "$(understudy env)"` in effect.

## String parameters

```sh
aws ssm put-parameter --name /orders/db/host --type String --value db.internal
aws ssm get-parameter --name /orders/db/host
```

```text
{
    "Version": 1,
    "Tier": "Standard"
}
{
    "Parameter": {
        "Name": "/orders/db/host",
        "Type": "String",
        "Value": "db.internal",
        "Version": 1,
        "LastModifiedDate": "2026-10-27T17:28:48.104000+00:00",
        "ARN": "arn:aws:ssm:us-east-1:000000000000:parameter/orders/db/host",
        "DataType": "text"
    }
}
```

Writing the name again needs `--overwrite`, which adds version 2. Without it the put fails, as on AWS:

```text
An error occurred (ParameterAlreadyExists) when calling the PutParameter operation: The parameter already exists. To overwrite this value, set the overwrite option in the request to true.
```

A parameter keeps its last 100 versions. A name with more than one level must begin with a slash, and a first level beginning with `aws` or `ssm`, in any case, is reserved: understudy refuses `/ssm-config` with `ValidationException`. AWS documents the reservation, but which error it answers, and whether it catches a reserved first level after a leading slash, is unrecorded (SSM-04).

## SecureString

A `SecureString` is encrypted through understudy's KMS. Without `--key-id`, the key is the AWS managed key `alias/aws/ssm`, created in the account and Region on first use:

```sh
aws ssm put-parameter --name /orders/db/password --type SecureString --value hunter2
aws ssm get-parameter --name /orders/db/password --with-decryption --query Parameter.Value --output text
```

```text
hunter2
```

Without `--with-decryption` the value is the base64 KMS ciphertext, which KMS `Decrypt` opens with the parameter's ARN as encryption context, as on AWS:

```sh
aws kms decrypt --encryption-context PARAMETER_ARN=arn:aws:ssm:us-east-1:000000000000:parameter/orders/db/password \
  --ciphertext-blob "$(aws ssm get-parameter --name /orders/db/password --query Parameter.Value --output text)" \
  --query Plaintext --output text
```

```text
aHVudGVyMg==
```

`--key-id` takes a customer managed key as a key id, key ARN, alias name or alias ARN, and `describe-parameters` reports it as given. A KMS refusal, such as a disabled key, answers `InvalidKeyId` with KMS's message:

```text
An error occurred (InvalidKeyId) when calling the GetParameter operation: arn:aws:kms:us-east-1:000000000000:key/4ae7c0e1-bc0c-4664-b9df-e736b2cfde41 is disabled.
```

The plaintext of a SecureString never appears in the journal, the logs or a snapshot, which holds only the ciphertext.

## By path, and several at once

`get-parameters-by-path` lists one level below the path, or every level with `--recursive`. Results come in name order, 10 to a page; the AWS CLI follows the pages:

```sh
aws ssm get-parameters-by-path --path /orders/db --query 'Parameters[].Name'
aws ssm get-parameters-by-path --path /orders --recursive --query 'Parameters[].[Name,Type,Version]' --output text
```

```text
[
    "/orders/db/host",
    "/orders/db/password",
    "/orders/db/port"
]
/orders/db/host	String	2
/orders/db/password	SecureString	1
/orders/db/port	String	1
/orders/queue/url	String	1
```

`get-parameters` reads up to ten names and lists those that do not exist in `InvalidParameters` instead of failing:

```sh
aws ssm get-parameters --names /orders/db/host /orders/missing --query InvalidParameters
```

```text
[
    "/orders/missing"
]
```

`describe-parameters` returns metadata without values and takes the `Name`, `Type`, `KeyId`, `Path`, `Tier`, `DataType` and `tag:` filters with their options.

## Versions and labels

A name followed by `:<version>` or `:<label>` reads that version. A label is attached to one version of a parameter at a time:

```sh
aws ssm label-parameter-version --name /orders/db/host --parameter-version 1 --labels stable
aws ssm get-parameter --name /orders/db/host:stable --query 'Parameter.[Version,Value,Selector]' --output text
aws ssm get-parameter-history --name /orders/db/host --query 'Parameters[].[Version,Value,join(`,`,Labels)]' --output text
```

```text
1	db.internal	:stable
1	db.internal	stable
2	db2.internal
```

`unlabel-parameter-version` removes labels; `get-parameters-by-path` takes a `Label` filter.

## Terraform

`aws_ssm_parameter` and the `aws_ssm_parameter` and `aws_ssm_parameters_by_path` data sources work with the provider block of the [Terraform guide](terraform.md). This configuration applied, planned no changes and destroyed cleanly with provider 6.68.0:

```hcl
resource "aws_ssm_parameter" "queue_url" {
  name  = "/orders/queue/url"
  type  = "String"
  value = "http://localhost:4566/000000000000/orders"
}

resource "aws_ssm_parameter" "db_password" {
  name  = "/orders/db/password"
  type  = "SecureString"
  value = "hunter2"
  tags  = { team = "orders" }
}

data "aws_ssm_parameters_by_path" "orders" {
  path       = "/orders"
  recursive  = true
  depends_on = [aws_ssm_parameter.queue_url, aws_ssm_parameter.db_password]
}

data "aws_ssm_parameter" "db_password" {
  name = aws_ssm_parameter.db_password.name
}
```

The provider reads `key_id` back as `alias/aws/ssm` for the default key.

## What answers 501

Each of these answers `NotImplemented` naming the feature, and changes nothing:

- AWS public parameters, any name or path under `/aws/service/`, such as the latest Amazon Linux AMI id.
- Secrets Manager references, names under `/aws/reference/secretsmanager/`. Read the secret from Secrets Manager instead.
- Parameter policies (`--policies`: expiration and notification).
- `--data-type aws:ec2:image`, since understudy has no EC2 to check the AMI.
- An Advanced-tier SecureString, including an `Intelligent-Tiering` one that resolves to Advanced. Advanced String and StringList parameters work.
- An `--allowed-pattern` Go's RE2 cannot compile, such as one with a lookahead.
- `describe-parameters`' deprecated `Filters` member; use `--parameter-filters`.
- The service settings, including the default parameter tier, and the resource policies that share parameters across accounts.

```text
An error occurred (NotImplemented) when calling the GetParameter operation: understudy does not implement AWS public parameters (names under /aws/service/) in ssm GetParameter; nothing was read or changed. Run 'understudy coverage --service ssm' for what is supported; to ask for it, see https://github.com/sustinbebustin/understudy/issues?q=ssm
```

Parameter Store sends no EventBridge `Parameter Store Change` events.

## The legacy Query protocol

AWS also serves SSM over the Query protocol: a POST with `?Action=GetParameter&Name=...` in the query string and no `X-Amz-Target`, which an API Gateway REST API's AWS integration can send. understudy does not, and answers 501 in SSM's JSON format:

```sh
curl -s --aws-sigv4 aws:amz:us-east-1:ssm --user test:test -X POST \
  "http://127.0.0.1:4566/?Action=GetParameter&Name=%2Forders%2Fdb%2Fhost"
```

```text
{"__type":"NotImplemented","message":"understudy does not serve the legacy Query protocol (?Action=GetParameter without X-Amz-Target) for ssm, which AWS also accepts. Send the JSON 1.1 request instead, with X-Amz-Target: AmazonSSM.GetParameter, as the AWS SDKs and CLI do. Nothing was changed."}
```

The same request with the parameters in a form body (`Content-Type: application/x-www-form-urlencoded`) answers the same 501, naming `AmazonSSM.<Action>` since understudy does not read the body. AWS's answer to that form is unrecorded:

```sh
curl -s --aws-sigv4 aws:amz:us-east-1:ssm --user test:test -X POST \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data 'Action=GetParameter&Name=%2Forders%2Fdb%2Fhost' http://127.0.0.1:4566/
```

```text
{"__type":"NotImplemented","message":"understudy does not serve the legacy Query protocol (a form body without X-Amz-Target) for ssm, which AWS also accepts. Send the JSON 1.1 request instead, with X-Amz-Target: AmazonSSM.<Action>, as the AWS SDKs and CLI do. Nothing was changed."}
```

The AWS SDKs and the AWS CLI send JSON 1.1 and are unaffected.
