# iam-loop

The least-privilege loop, end to end: what a workload needs is recorded under `--iam-enforce advise`, rendered as policies, attached as customer managed policies, and checked under `--iam-enforce enforce`. The workload is the aws-cli and go-sdk suites, one group per test file, each group run as its own IAM user the account root mints.

```sh
conformance/iam-loop/run.sh
IAM_LOOP_GROUPS='^aws-cli-(sqs|sns)$' IAM_LOOP_ROOT_SUITES=0 conformance/iam-loop/run.sh
IAM_LOOP_GROUPS='^$' conformance/iam-loop/run.sh      # only (a), (e) and the root suites
IAM_LOOP_JOBS=4 conformance/iam-loop/run.sh           # up to four groups at a time
CONFORMANCE_DYNAMODB=0 conformance/iam-loop/run.sh    # skip DynamoDB's tests
```

It needs everything the aws-cli and go-sdk suites need (the AWS CLI, Go, jq, curl, python3, Docker for Lambda), and Terraform and the examples' tools for the last step.

| Variable | Default | Meaning |
|---|---|---|
| `IAM_LOOP_GROUPS` | unset: every group | Extended regex (`grep -E`, unanchored) over group names. `'^$'` selects none. An invalid regex exits 2. |
| `IAM_LOOP_JOBS` | `1` | Groups run at most this many at a time, in the advise phase and in the enforce phase; a positive integer, else exit 2. A group whose file moves or reads the emulator clock (`/clock`, `frozenClock(`, `advanceClock(`) runs alone: the clock is one per server, and a reset rewinds it. |
| `IAM_LOOP_ROOT_SUITES` | `1` | `0` leaves step 6 out; its line reads `SKIP root-suites`. |
| `CONFORMANCE_NAME_SEED` | `iam-loop-<8 hex>` | One seed for every group; resource names already include the test. |
| `CONFORMANCE_DYNAMODB`, `CONFORMANCE_PORT` | | As in the other suites. |

Exit 0 when every selected group and global check passes, 1 when any fails, 2 for a missing tool, an invalid variable, or a go-sdk test the binary lists that no file declares. The work directory is kept on failure; its path is printed.

## Groups

- aws-cli: one group per `conformance/aws-cli/tests/<stem>.sh`, named `aws-cli-<stem>` (`aws-cli-sqs`, `aws-cli-sns-http`), run as `run-suite.sh <stem>`.
- go-sdk: one group per `conformance/go-sdk/<stem>_test.go` that declares a test the binary lists, named `go-sdk-<stem>` (`go-sdk-sns_http`). The test binary is built once (`go test -c`); its list (`-test.list '.*'`) maps each test to the file holding `func <Name>(`, and a group runs the binary with `-test.run '^(T1|T2|...)$'`. Files of shared helpers make no group.
- Order: aws-cli groups by stem, then go-sdk groups by stem.
- Group `<g>` runs as the user `iam-loop-<g>`. Its roles are the roles that user created (`iam:CreateRole` in its advise-run usage).
- Per group, the work directory holds `<g>/advise.log`, `<g>/enforce.log`, `<g>/user-policy.json`, `<g>/roles/<role>.json` and the split chunks under `<g>/chunks/`.

## Steps

1. Build understudy and the go-sdk test binary; resolve the selected groups.
2. Serve with `--iam-enforce advise` on a fresh data directory (skipped when no group is selected). Per group, the root (the dummy key `test`) creates the user and an access key, and the group runs signed with that key, with `CONFORMANCE_NAME_SEED` set, so every resource name is a function of the seed, the test, the prefix and a per-test counter (aws-cli `new_name`, go-sdk `name`).
3. Per group, fetch `GET /iam/policy` for the user (a 404 means no document: nothing attached) and for each of its roles; a role answering 404 is skipped with the line `run.sh: <role arn> has no policy to attach: its recorded calls need no permission`. Each document is split into managed policies (below).
4. Serve again, fresh, with `--iam-enforce enforce`. Per group, the root creates the user and a key; unless the group is over a quota, it creates each chunk as a customer managed policy (`iam create-policy`), attaches the user's (`iam attach-user-policy`) and lists each role's ARNs in `<work>/roles/<role name>.arns`. The group runs again with the same seed and `CONFORMANCE_ROLE_POLICY_DIR=<work>/roles`: right after a suite's role helper (aws-cli `make_role`, go-sdk `createRole`) creates a role with a file there, the root attaches each listed policy to it (`iam attach-role-policy`).
5. Check:
   - (a) `GET /iam/usage` for the root lists only `iam:CreateUser`, `iam:CreateAccessKey`, `iam:CreatePolicy`, `iam:AttachUserPolicy` and `iam:AttachRolePolicy`: no test ran as the root. Global line `root-calls`.
   - (b) per group, the enforce run passes and the `access-denied` advisories of the group's principals equal its expected denials (below). An advisory belongs to the group when it names the group's user, one of its roles (`:role/<name>`) or a session of one (`:assumed-role/<name>/`); principal, action and resource are read from the advisory text `... for <principal> calling <action> on <resource>`. A denial of no selected group fails the global line `unattributed-denials`.
   - (c) per group, `POST /iam/evaluate` for the user with `identityPolicies: []` denies every pair its advise run recorded (a bucket's objects as `arn:aws:s3:::<bucket>/*`).
   - (d) per group, for each statement of the user's policy, the policy without it denies at least one recorded pair whose action the statement lists; the denial's message is printed. A statement whose pairs a resource-based policy alone allows gets no exemption: those pairs already fail (c).
   - (e) a user with no policy is refused by each service. STS's message is compared with the recorded form (`testdata/parity/sts/R15/06-assume-denied.json`, iam-enforcement-surfaces.md 4.3) and S3's with the recorded one (`testdata/parity/s3/IAM-ENF/12-list.json`, 4.2); the other services' codes are printed beside the switch that sets them (12.8), since no recording fixes them. Global line `probes`.
6. Unless `IAM_LOOP_ROOT_SUITES=0`, run `conformance/terraform/run.sh` and `examples/check.sh` as the root with `CONFORMANCE_IAM_ENFORCE=enforce` and `CONFORMANCE_ADVISORIES_OUT`: no `access-denied` advisory may name a resource-based policy or a role trust policy as its decider, or a service principal as its caller. Global line `root-suites`.

## Expected denials

Some tests assert an AccessDenied that AWS answers too; `expected-denials.tsv` lists them. It is tab-separated: `#` lines and blank lines are ignored, the first other line is the header `group<TAB>test<TAB>action<TAB>resource<TAB>source` (tabs between the names), and each row has five non-empty fields:

| Column | Content |
|---|---|
| `group` | the group (above) |
| `test` | the test asserting the denial; informational, since the journal does not know tests |
| `action` | the IAM action, compared exactly |
| `resource` | an extended regex matched against the whole resource text (`^(...)$`) |
| `source` | the citation, `<note>.md <section>` |

A malformed file exits 2. For a selected group, (b) fails on each of its advisories that matches no row of the group (`unexpected denial`) and on each row of the group that matches none of its advisories (`expected denial did not happen`); rows of unselected groups are ignored.

## Managed policies and quotas

A user holds at most 10 attached managed policies, a role 20, and one managed policy document at most 6,144 characters, white space not counted; a policy name is at most 128 characters (iam-sts-kms.md 3.2). The loop raises no quota:

- A document's size is its characters other than white space (`internal/service/iam/documents.go` `policySize`).
- Its statements are split greedily, in the rendered order: the next statement joins the current chunk `{"Version":"2012-10-17","Statement":[...]}` while the chunk stays within 6,144 characters, else it starts a new one.
- Names: the user's chunks `iam-loop-<g>-<n>`, a role's `iam-loop-<g>-<role name>-<n>`, `n` from 1.
- A statement alone over 6,144 characters, more than 10 chunks for the user or 20 for a role, or a name over 128 characters is a failed check of the group, naming the principal, its statement count and size; the group's enforce run is not made.

## Output

On stdout, after every check, one line per selected group in group order, then the global lines, each followed by its failed checks indented two spaces:

```
PASS aws-cli-sqs statements=25 chars=3105 policies=1
FAIL aws-cli-sts statements=7 chars=745 policies=1
  (b) unexpected denial: User: arn:aws:iam::000000000000:user/iam-loop-aws-cli-sts is not authorized to perform: sts:AssumeRole on resource: ...
PASS root-calls
PASS unattributed-denials
PASS probes
SKIP root-suites
```

`statements` and `chars` describe the user's document, `policies` its chunk count. A failed check is one of `(quota)`, `(name)`, `(b)`, `(c)`, `(d)`. Notes and progress go to stderr, whose last line is `run.sh: checks failed: <n>`.

## Batches

One invocation of every group can outrun a 10-minute limit on 4 vCPU, so the loop is verified as several invocations, each selecting a subset of groups with `IAM_LOOP_GROUPS` and `IAM_LOOP_ROOT_SUITES=0`, plus one `IAM_LOOP_GROUPS='^$'` invocation for (e) and step 6. A green claim names every batch, its group regex, its result and the commit; the union of the batches covers every group.

## Tests that need the root

The suites sign as the root everywhere else, so a few tests assert it, or assert an answer only the root gets. They are skipped by name in both runs:

| Suite | Test | Why |
|---|---|---|
| aws-cli | `test_get_caller_identity_static_key_is_root` | asserts the caller is `arn:aws:iam::<account>:root` |
| aws-cli | `test_alias_for_missing_key` | asserts `NotFoundException` for a key id that names no key; for a caller other than the root, a missing key has no key policy to allow `kms:CreateAlias`, and understudy denies first (`iam.enforce.missing-resource` `deny-first`, iam-enforcement-surfaces.md 5.3; AWS's order is UNVERIFIED, IAM-ENF-14) |
| go-sdk | `TestSTSGetCallerIdentity` | asserts the caller is `arn:aws:iam::<account>:root` |

A test added to this list says why here.
