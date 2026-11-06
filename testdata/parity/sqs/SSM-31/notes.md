# SSM-31 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.32; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

Session change: the first run received 4 of 5 events in its four receives (Create missing) and its teardown then deleted the queue; those captures are in `$REC/_work/SSM-31-run1`. This capture is a full rerun of the case whose receive repeats until five distinct messages arrive (at most 12 receives), as the recipe says.

- Five events (create, update, label, label move, delete): matches: all five arrived, one or two per receive.
- Envelope: `version "0"`, `source aws.ssm`, `detail-type Parameter Store Change`, `account`, `region us-east-2`, `resources: ["arn:aws:ssm:us-east-2:<acct>:parameter/us1103/ssm/e31"]` (the parameter ARN).
- detail fields: `name`, `description`, `type`, `operation` on every event; the label events add `label`, `fromVersion`, `toVersion`.
- `operation` values: `Create`, `Update` (the overwrite), `LabelParameterVersion` (both label calls), `Delete`: matches 9.2.
- First label (latest, version 2): `fromVersion: ""`, `toVersion: "2"` (strings). Move to version 1: `fromVersion: "2"`, `toVersion: "1"`.
- The overwrite's event still carries `description: "d"`, the kept description.
- understudy sends no events (12.6), so this is AWS fact only.
