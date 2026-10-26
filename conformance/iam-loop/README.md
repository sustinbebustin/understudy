# iam-loop

The least-privilege loop, end to end: what a workload needs is recorded under `--iam-enforce advise`, rendered as policies, attached, and checked under `--iam-enforce enforce`. The workload is the aws-cli and go-sdk suites, run as an IAM user the account root mints.

```sh
conformance/iam-loop/run.sh
CONFORMANCE_DYNAMODB=0 conformance/iam-loop/run.sh   # skip DynamoDB's tests
IAM_LOOP_ROOT_SUITES=0 conformance/iam-loop/run.sh   # skip the Terraform suite and the examples
```

It needs everything the aws-cli and go-sdk suites need (the AWS CLI, Go, jq, curl, python3, Docker for Lambda), and Terraform and the examples' tools for the last step.

## Steps

1. Serve with `--iam-enforce advise` on a fresh data directory. The root (the dummy key `test`) creates the user `iam-loop` and an access key.
2. Run both suites signed with that key, with `CONFORMANCE_NAME_SEED` set, so every resource name is a function of the seed, the test, the prefix and a per-test counter (aws-cli `new_name`, go-sdk `name`).
3. Fetch `GET /iam/policy` for the user, and for every role principal `GET /iam/usage` lists; print them.
4. Serve again, fresh, with `--iam-enforce enforce`. The root creates the user and a key and puts the user's policy as inline policy `iam-loop`. Re-run both suites with the same seed and `CONFORMANCE_ROLE_POLICY_DIR` holding the role policies: right after a suite's role helper (aws-cli `make_role`, go-sdk `createRole`) creates a role with a file there, the root puts it as the role's inline policy `iam-loop`.
5. Check:
   - (a) `GET /iam/usage` for the root lists only `iam:CreateUser`, `iam:CreateAccessKey`, `iam:PutUserPolicy` and `iam:PutRolePolicy`: no test ran as the root.
   - (b) both suites pass, and the journal holds no `access-denied` advisory.
   - (c) `POST /iam/evaluate` for the user with `identityPolicies: []` denies every pair `GET /iam/usage` recorded for it (a bucket's objects as `arn:aws:s3:::<bucket>/*`).
   - (d) for each statement of the user's policy, the policy without it denies at least one recorded pair whose action the statement lists; the denial's message is printed.
   - (e) a second user with no policy is refused by each service. STS's message is compared with the recorded form (`testdata/parity/sts/R15/06-assume-denied.json`, iam-enforcement-surfaces.md 4.3) and S3's with the documented one (4.2); the other services' codes are printed beside the switch that sets them (12.8), since no recording fixes them.
6. Run `conformance/terraform/run.sh` and `examples/check.sh` as the root with `CONFORMANCE_IAM_ENFORCE=enforce` and `CONFORMANCE_ADVISORIES_OUT`: no `access-denied` advisory may name a resource-based policy or a role trust policy as its decider, or a service principal as its caller.

## Tests skipped as the root

The suites sign as the root everywhere else, so a few tests assert it. They are skipped by name in both runs:

| Suite | Test | Why |
|---|---|---|
| aws-cli | `test_get_caller_identity_static_key_is_root` | asserts the caller is `arn:aws:iam::<account>:root` |
| go-sdk | `TestSTSGetCallerIdentity` | asserts the caller is `arn:aws:iam::<account>:root` |

A test added to this list says why here.
