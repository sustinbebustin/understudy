package access_test

import (
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

const (
	roleARN   = "arn:aws:iam::111111111111:role/worker"
	userARN   = "arn:aws:iam::111111111111:user/app"
	queueARN  = "arn:aws:sqs:us-east-1:111111111111:orders"
	topicARN  = "arn:aws:sns:us-east-1:111111111111:events"
	groupARN  = "arn:aws:logs:us-east-1:111111111111:log-group:/aws/lambda/worker"
	streamARN = "arn:aws:logs:us-east-1:111111111111:log-group:/aws/lambda/worker:log-stream:2026/10/08/[$LATEST]0123456789abcdef"
	keyARN    = "arn:aws:kms:us-east-1:111111111111:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	objectARN = "arn:aws:s3:::uploads/report.csv"
)

// t0 is an arbitrary emulator instant.
var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

var testAccount = func() aws.AccountID {
	a, err := aws.ParseAccountID("111111111111")
	if err != nil {
		panic(err)
	}
	return a
}()

// The principals of roleARN and userARN, and two delivery services.
var (
	worker = access.Role{Account: testAccount, Name: "worker"}
	app    = access.User{Account: testAccount, Name: "app"}
	sns    = access.ServicePrincipal{Name: "sns.amazonaws.com"}
	events = access.ServicePrincipal{Name: "events.amazonaws.com"}
)

func arn(t *testing.T, s string) aws.ARN {
	t.Helper()
	a, err := aws.ParseARN(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func act(prefix, name string) registry.IAMAction {
	return registry.IAMAction{Prefix: prefix, Name: name}
}

// record records every entry, failing on a refusal.
func record(t *testing.T, r *access.Recorder, entries ...access.Entry) {
	t.Helper()
	for _, e := range entries {
		if err := r.Record(e); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
}

// userPolicy returns the encoded Policy of app, failing when there is none.
func userPolicy(t *testing.T, r *access.Recorder) string {
	t.Helper()
	doc, ok := r.Policy(app)
	if !ok {
		t.Fatalf("Policy(%s) reports no policy", app)
	}
	return string(policydoc.Encode(doc))
}

// actionsOf returns every action a document's Allow statements name.
func actionsOf(doc policydoc.Document) []string {
	var out []string
	for _, st := range doc.Statements {
		if a, ok := st.Action.(policydoc.Action); ok && st.Effect == policydoc.Allow {
			out = append(out, a...)
		}
	}
	return out
}

// The managed policies the generated policy of a Lambda worker fed by an SQS
// event source mapping must cover (ARCHITECTURE.md 19).
const (
	awsLambdaSQSQueueExecutionRole = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:ReceiveMessage","sqs:DeleteMessage",
"sqs:GetQueueAttributes","logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents"],"Resource":"*"}]}`
	awsLambdaBasicExecutionRole = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream",
"logs:PutLogEvents"],"Resource":"*"}]}`
)

// An ESM worker session: the poller receives, deletes (DeleteMessageBatch
// maps to sqs:DeleteMessage) and reads queue attributes as the execution
// role, Lambda writes the function's logs as the role, and one delete fails
// with a modeled error the application handles. Calls by other principals
// and SNS deliveries do not leak into the role's policy.
func TestPolicyCoversLambdaSQSExecutionRoles(t *testing.T) {
	var r access.Recorder
	queue, group, stream := arn(t, queueARN), arn(t, groupARN), arn(t, streamARN)
	role := func(mode access.Mode, action registry.IAMAction, res aws.ARN, out access.Outcome) access.Entry {
		return access.Entry{Principal: worker, Mode: mode, Actions: []registry.IAMAction{action}, Resources: []aws.ARN{res}, Outcome: out, At: t0}
	}
	record(t, &r,
		role(access.AsExecutionRole{}, act("sqs", "GetQueueAttributes"), queue, access.Succeeded{}),
		role(access.AsExecutionRole{}, act("sqs", "ReceiveMessage"), queue, access.Succeeded{}),
		role(access.AsExecutionRole{}, act("sqs", "DeleteMessage"), queue, access.Succeeded{}),
		role(access.AsExecutionRole{}, act("logs", "CreateLogGroup"), group, access.Succeeded{}),
		role(access.AsExecutionRole{}, act("logs", "CreateLogStream"), group, access.Succeeded{}),
		role(access.AsExecutionRole{}, act("logs", "PutLogEvents"), stream, access.Succeeded{}),
		role(access.Direct{}, act("sqs", "ReceiveMessage"), queue, access.Succeeded{}),
		role(access.Direct{}, act("sqs", "ChangeMessageVisibility"), queue, access.Failed{Code: "ReceiptHandleIsInvalid"}),
		access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("sqs", "PurgeQueue")}, Resources: []aws.ARN{queue}, Outcome: access.Succeeded{}, At: t0},
		access.Entry{Principal: sns, Mode: access.AsService{Source: arn(t, topicARN)}, Actions: []registry.IAMAction{act("sqs", "SendMessage")}, Resources: []aws.ARN{queue}, Outcome: access.Succeeded{}, At: t0},
	)

	got, ok := r.Policy(worker)
	if !ok {
		t.Fatal("Policy reports no policy for the role")
	}
	want := `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream"],"Resource":"` + groupARN + `"},` +
		`{"Effect":"Allow","Action":"logs:PutLogEvents","Resource":"` + streamARN + `"},` +
		`{"Effect":"Allow","Action":["sqs:ChangeMessageVisibility","sqs:DeleteMessage","sqs:GetQueueAttributes","sqs:ReceiveMessage"],"Resource":"` + queueARN + `"}]}`
	if enc := string(policydoc.Encode(got)); enc != want {
		t.Errorf("Policy =\n%s\nwant\n%s", enc, want)
	}
	if err := policydoc.ValidateIdentity(got); err != nil {
		t.Errorf("the generated policy is not a valid identity policy: %v", err)
	}
	generated := actionsOf(got)
	for _, managed := range []string{awsLambdaSQSQueueExecutionRole, awsLambdaBasicExecutionRole} {
		doc, err := policydoc.Parse([]byte(managed))
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range actionsOf(doc) {
			if !slices.Contains(generated, a) {
				t.Errorf("the generated policy lacks %s", a)
			}
		}
	}
}

// A function's own code calls as a session of its execution role, one
// session per execution environment, while AWS polls and writes logs as the
// role itself. Every one of those calls belongs to the role's policy, which
// is found by the role's IAM ARN, path included, however the caller spelled
// a root path.
func TestPolicyMergesEverySessionOfARoleWithItsExecutionRoleCalls(t *testing.T) {
	var r access.Recorder
	queue, object, key := arn(t, queueARN), arn(t, objectARN), arn(t, keyARN)
	pathed := access.Role{Account: testAccount, Path: "/service-role/", Name: "worker"}
	rootPath := access.Role{Account: testAccount, Path: "/", Name: "worker"}
	record(t, &r,
		access.Entry{Principal: worker, Mode: access.AsExecutionRole{}, Actions: []registry.IAMAction{act("sqs", "ReceiveMessage")}, Resources: []aws.ARN{queue}, Outcome: access.Succeeded{}, At: t0},
		access.Entry{Principal: rootPath, Mode: access.Direct{}, Actions: []registry.IAMAction{act("s3", "PutObject")}, Resources: []aws.ARN{object}, Outcome: access.Succeeded{}, At: t0},
		access.Entry{Principal: worker, Mode: access.AsCaller{}, Actions: []registry.IAMAction{act("kms", "GenerateDataKey")}, Resources: []aws.ARN{key}, Outcome: access.Succeeded{}, At: t0},
		access.Entry{Principal: pathed, Mode: access.Direct{}, Actions: []registry.IAMAction{act("sqs", "SendMessage")}, Resources: []aws.ARN{queue}, Outcome: access.Succeeded{}, At: t0},
	)

	parsed, err := access.ParsePrincipal(roleARN)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := parsed.(access.Identity)
	if !ok {
		t.Fatalf("ParsePrincipal(%s) = %#v, want an Identity", roleARN, parsed)
	}
	got, ok := r.Policy(id)
	if !ok {
		t.Fatal("Policy reports no policy for the role")
	}
	want := `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Action":"kms:GenerateDataKey","Resource":"` + keyARN + `"},` +
		`{"Effect":"Allow","Action":"s3:PutObject","Resource":"` + objectARN + `"},` +
		`{"Effect":"Allow","Action":"sqs:ReceiveMessage","Resource":"` + queueARN + `"}]}`
	if enc := string(policydoc.Encode(got)); enc != want {
		t.Errorf("Policy(%s) =\n%s\nwant\n%s", roleARN, enc, want)
	}

	got, ok = r.Policy(pathed)
	want = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"` + queueARN + `"}]}`
	if enc := string(policydoc.Encode(got)); !ok || enc != want {
		t.Errorf("Policy(%s) = %s, %t; want %s: a role with a path is another role", pathed, enc, ok, want)
	}
}

// S3 calls KMS as the caller for SSE-KMS (iam-sts-kms.md 5.8): PutObject
// needs kms:GenerateDataKey and GetObject kms:Decrypt.
func TestPolicyCarriesForwardAccessKMSActions(t *testing.T) {
	var r access.Recorder
	object, key := arn(t, objectARN), arn(t, keyARN)
	user := func(mode access.Mode, action registry.IAMAction, res aws.ARN) access.Entry {
		return access.Entry{Principal: app, Mode: mode, Actions: []registry.IAMAction{action}, Resources: []aws.ARN{res}, Outcome: access.Succeeded{}, At: t0}
	}
	record(t, &r,
		user(access.AsCaller{}, act("kms", "GenerateDataKey"), key),
		user(access.Direct{}, act("s3", "PutObject"), object),
		user(access.AsCaller{}, act("kms", "Decrypt"), key),
		user(access.Direct{}, act("s3", "GetObject"), object),
	)

	want := `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Action":["kms:Decrypt","kms:GenerateDataKey"],"Resource":"` + keyARN + `"},` +
		`{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],"Resource":"` + objectARN + `"}]}`
	if got := userPolicy(t, &r); got != want {
		t.Errorf("Policy =\n%s\nwant\n%s", got, want)
	}
}

// An S3 key may hold * and ?, which a policy Resource reads as wildcards;
// the policy escapes them, so it grants the touched object and no other.
func TestPolicyEscapesWildcardsInResourceNames(t *testing.T) {
	var r access.Recorder
	object := arn(t, "arn:aws:s3:::uploads/*/report?.csv")
	record(t, &r, access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("s3", "GetObject")}, Resources: []aws.ARN{object}, Outcome: access.Succeeded{}, At: t0})

	want := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::uploads/${*}/report${?}.csv"}]}`
	if got := userPolicy(t, &r); got != want {
		t.Errorf("Policy =\n%s\nwant\n%s", got, want)
	}
	doc, _ := r.Policy(app)
	for resource, want := range map[string]bool{
		object.String(): true,
		"arn:aws:s3:::uploads/secret/report1.csv": false,
	} {
		if got, ok := policydoc.Allows(doc, policydoc.Query{Action: "s3:GetObject", Resource: resource}).(policydoc.Allowed); ok != want {
			t.Errorf("Policy allows %s: %t (%#v), want %t", resource, ok, got, want)
		}
	}
}

// CopyObject maps to s3:GetObject and s3:PutObject and touches two objects;
// ListBuckets has no resource.
func TestPolicyCoversMultiActionAndResourcelessOperations(t *testing.T) {
	var r access.Recorder
	src, dst := arn(t, "arn:aws:s3:::in/a"), arn(t, "arn:aws:s3:::out/a")
	record(t, &r,
		access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("s3", "GetObject"), act("s3", "PutObject")}, Resources: []aws.ARN{src, dst}, Outcome: access.Succeeded{}, At: t0},
		access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("s3", "ListAllMyBuckets")}, Outcome: access.Succeeded{}, At: t0},
	)

	want := `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],"Resource":"arn:aws:s3:::in/a"},` +
		`{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],"Resource":"arn:aws:s3:::out/a"},` +
		`{"Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*"}]}`
	if got := userPolicy(t, &r); got != want {
		t.Errorf("Policy =\n%s\nwant\n%s", got, want)
	}
}

// IAM authorizes an action only against its own service's resources:
// CreateFunction's iam:PassRole is granted on "*" when the call touched
// only the function, and on the role when it touched the role; STS actions
// act on IAM roles.
func TestPolicyPairsActionsWithTheirServicesResources(t *testing.T) {
	var r access.Recorder
	fn, role := arn(t, "arn:aws:lambda:us-east-1:111111111111:function:worker"), arn(t, roleARN)
	record(t, &r,
		access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("lambda", "CreateFunction"), act("iam", "PassRole")}, Resources: []aws.ARN{fn}, Outcome: access.Succeeded{}, At: t0},
		access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("sts", "AssumeRole")}, Resources: []aws.ARN{role}, Outcome: access.Succeeded{}, At: t0},
	)
	want := `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"},` +
		`{"Effect":"Allow","Action":"lambda:CreateFunction","Resource":"` + fn.String() + `"},` +
		`{"Effect":"Allow","Action":"sts:AssumeRole","Resource":"` + roleARN + `"}]}`
	if got := userPolicy(t, &r); got != want {
		t.Errorf("Policy =\n%s\nwant\n%s", got, want)
	}

	var withRole access.Recorder
	record(t, &withRole, access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("lambda", "CreateFunction"), act("iam", "PassRole")}, Resources: []aws.ARN{fn, role}, Outcome: access.Succeeded{}, At: t0})
	want = `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Action":"iam:PassRole","Resource":"` + roleARN + `"},` +
		`{"Effect":"Allow","Action":"lambda:CreateFunction","Resource":"` + fn.String() + `"}]}`
	if got := userPolicy(t, &withRole); got != want {
		t.Errorf("Policy with the role touched =\n%s\nwant\n%s", got, want)
	}
}

// A principal without recorded use, or no principal at all, has no policy:
// IAM rejects a document without statements, and a nil principal must not
// merge every principal's use.
func TestPolicyOfAPrincipalWithoutUseIsNone(t *testing.T) {
	var r access.Recorder
	record(t, &r, access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("sqs", "SendMessage")}, Resources: []aws.ARN{arn(t, queueARN)}, Outcome: access.Succeeded{}, At: t0})
	for _, principal := range []access.Identity{worker, nil} {
		if doc, ok := r.Policy(principal); ok {
			t.Errorf("Policy(%v) = %s, want no policy", principal, policydoc.Encode(doc))
		}
	}
}

// Record refuses an entry it would otherwise have to guess about, and
// records nothing of it.
func TestRecordRefusesIncompleteOrMismatchedEntries(t *testing.T) {
	queue := arn(t, queueARN)
	valid := access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("sqs", "SendMessage")}, Resources: []aws.ARN{queue}, Outcome: access.Succeeded{}, At: t0}
	tests := []struct {
		name string
		edit func(*access.Entry)
		want access.EntryProblem
	}{
		{"no principal", func(e *access.Entry) { e.Principal = nil }, access.EntryNoPrincipal},
		{"no mode", func(e *access.Entry) { e.Mode = nil }, access.EntryNoMode},
		{"no outcome", func(e *access.Entry) { e.Outcome = nil }, access.EntryNoOutcome},
		{"no instant", func(e *access.Entry) { e.At = time.Time{} }, access.EntryNoInstant},
		{"delivery by an IAM identity", func(e *access.Entry) { e.Mode = access.AsService{Source: arn(t, topicARN)} }, access.EntryModeMismatch},
		{"direct call by a service principal", func(e *access.Entry) { e.Principal = sns }, access.EntryModeMismatch},
		{"zero role", func(e *access.Entry) { e.Principal = access.Role{} }, access.EntryInvalidPrincipal},
		{"zero root", func(e *access.Entry) { e.Principal = access.Root{} }, access.EntryInvalidPrincipal},
		{"path without slashes", func(e *access.Entry) {
			e.Principal = access.Role{Account: testAccount, Path: "service-role", Name: "w"}
		}, access.EntryInvalidPrincipal},
		{"name with a slash", func(e *access.Entry) {
			e.Principal = access.Role{Account: testAccount, Path: "/a/", Name: "b/c"}
		}, access.EntryInvalidPrincipal},
		{"zero service principal", func(e *access.Entry) {
			e.Principal = access.ServicePrincipal{}
			e.Mode = access.AsService{Source: arn(t, topicARN)}
		}, access.EntryInvalidPrincipal},
		{"action without a prefix", func(e *access.Entry) { e.Actions = []registry.IAMAction{act("", "SendMessage")} }, access.EntryInvalidAction},
		{"action without a name", func(e *access.Entry) {
			e.Actions = []registry.IAMAction{act("sqs", "SendMessage"), act("sqs", "")}
		}, access.EntryInvalidAction},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var r access.Recorder
			e := valid
			tc.edit(&e)
			err := r.Record(e)
			var entryErr *access.EntryError
			if !errors.As(err, &entryErr) || entryErr.Problem != tc.want {
				t.Fatalf("Record = %v, want an EntryError with problem %d", err, tc.want)
			}
			if got := r.Usage(access.Filter{}); len(got) != 0 {
				t.Errorf("Record refused the entry but recorded %+v", got)
			}
		})
	}
}

func TestUsageAggregatesCalls(t *testing.T) {
	var r access.Recorder
	queue := arn(t, queueARN)
	send := func(at time.Time, out access.Outcome) access.Entry {
		return access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("sqs", "SendMessage")}, Resources: []aws.ARN{queue}, Outcome: out, At: at}
	}
	record(t, &r,
		send(t0.Add(time.Minute), access.Succeeded{}),
		send(t0, access.Failed{Code: "QueueDoesNotExist"}),
		send(t0.Add(2*time.Minute), access.Succeeded{}),
		access.Entry{Principal: worker, Mode: access.AsExecutionRole{}, Actions: []registry.IAMAction{act("sqs", "ReceiveMessage")}, Resources: []aws.ARN{queue}, Outcome: access.Succeeded{}, At: t0},
		access.Entry{Principal: worker, Mode: access.AsExecutionRole{}, Actions: []registry.IAMAction{act("logs", "PutLogEvents")}, Resources: []aws.ARN{arn(t, streamARN)}, Outcome: access.Succeeded{}, At: t0},
	)

	got := r.Usage(access.Filter{Principal: app})
	want := []access.Use{{
		Principal: access.User{Account: testAccount, Path: "/", Name: "app"}, Mode: access.Direct{}, Action: act("sqs", "SendMessage"), Resource: access.OnResource{ARN: queue},
		Calls: 3, Failures: 1, First: t0, Last: t0.Add(2 * time.Minute),
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Usage(principal) =\n%+v\nwant\n%+v", got, want)
	}
	if got := r.Usage(access.Filter{Service: "logs"}); len(got) != 1 || got[0].Action != act("logs", "PutLogEvents") {
		t.Errorf("Usage(service logs) = %+v, want the one PutLogEvents use", got)
	}
	if got := r.Usage(access.Filter{}); len(got) != 3 {
		t.Errorf("Usage(all) has %d uses, want 3: %+v", len(got), got)
	}
	r.Reset()
	if got := r.Usage(access.Filter{}); len(got) != 0 {
		t.Errorf("Usage after Reset = %+v, want none", got)
	}
}

// An Entry is one call however often it names a resource or an action:
// CopyObject onto its own source touches one object twice.
func TestUsageCountsAnEntryOncePerUse(t *testing.T) {
	var r access.Recorder
	object, queue := arn(t, objectARN), arn(t, queueARN)
	record(t, &r,
		access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("s3", "GetObject"), act("s3", "PutObject")}, Resources: []aws.ARN{object, object}, Outcome: access.Failed{Code: "AccessDenied"}, At: t0},
		access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("sqs", "DeleteMessage"), act("sqs", "DeleteMessage")}, Resources: []aws.ARN{queue}, Outcome: access.Failed{Code: "ReceiptHandleIsInvalid"}, At: t0},
	)

	got := r.Usage(access.Filter{Principal: app})
	if len(got) != 3 {
		t.Fatalf("Usage = %+v, want one use each of s3:GetObject, s3:PutObject and sqs:DeleteMessage", got)
	}
	for _, u := range got {
		if u.Calls != 1 || u.Failures != 1 {
			t.Errorf("use of %s on %v: Calls %d, Failures %d; want 1 and 1", u.Action, u.Resource, u.Calls, u.Failures)
		}
	}
}

func TestResourceRequirementsListDeliveries(t *testing.T) {
	var r access.Recorder
	queue, topic := arn(t, queueARN), arn(t, topicARN)
	rule := arn(t, "arn:aws:events:us-east-1:111111111111:rule/nightly")
	fn := arn(t, "arn:aws:lambda:us-east-1:111111111111:function:worker")
	for range 2 {
		record(t, &r, access.Entry{Principal: sns, Mode: access.AsService{Source: topic}, Actions: []registry.IAMAction{act("sqs", "SendMessage")}, Resources: []aws.ARN{queue}, Outcome: access.Succeeded{}, At: t0})
	}
	record(t, &r,
		access.Entry{Principal: events, Mode: access.AsService{Source: rule}, Actions: []registry.IAMAction{act("lambda", "InvokeFunction")}, Resources: []aws.ARN{fn}, Outcome: access.Succeeded{}, At: t0},
		access.Entry{Principal: worker, Mode: access.AsExecutionRole{}, Actions: []registry.IAMAction{act("sqs", "ReceiveMessage")}, Resources: []aws.ARN{queue}, Outcome: access.Succeeded{}, At: t0},
	)

	want := []access.Requirement{
		{Target: fn, Principal: events, Action: act("lambda", "InvokeFunction"), Source: rule},
		{Target: queue, Principal: sns, Action: act("sqs", "SendMessage"), Source: topic},
	}
	if got := r.ResourceRequirements(access.Filter{}); !reflect.DeepEqual(got, want) {
		t.Errorf("ResourceRequirements =\n%+v\nwant\n%+v", got, want)
	}
	if got := r.ResourceRequirements(access.Filter{Principal: sns}); !reflect.DeepEqual(got, want[1:]) {
		t.Errorf("ResourceRequirements(sns) = %+v, want %+v", got, want[1:])
	}
}

func TestRecorderIsSafeForConcurrentUse(t *testing.T) {
	var r access.Recorder
	queue := arn(t, queueARN)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				if err := r.Record(access.Entry{Principal: app, Mode: access.Direct{}, Actions: []registry.IAMAction{act("sqs", "SendMessage")}, Resources: []aws.ARN{queue}, Outcome: access.Succeeded{}, At: t0}); err != nil {
					t.Error(err)
					return
				}
				r.Usage(access.Filter{})
				r.Policy(app)
			}
		})
	}
	wg.Wait()
	if got := r.Usage(access.Filter{}); len(got) != 1 || got[0].Calls != 800 {
		t.Errorf("Usage = %+v, want one use with 800 calls", got)
	}
}
