package dynamodb_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
)

// usesOf lists principal's recorded dynamodb uses as "<action> <resource>",
// the resource as a policy writes it.
func usesOf(h *streamsHarness, principal access.Principal) []string {
	h.Helper()
	uses := h.Access.Usage(access.Filter{Principal: principal, Service: "dynamodb"})
	out := make([]string, 0, len(uses))
	for i := range uses {
		u := &uses[i]
		r := "*"
		if on, ok := u.Resource.(access.OnResource); ok {
			r = on.ARN.String()
		}
		out = append(out, u.Action.String()+" "+r)
	}
	return out
}

// The four Streams operations each need their own dynamodb action and no
// other, on the stream they address, ListStreams on "*". DescribeStream needs
// dynamodb:DescribeStream only, though the generated action list also
// names dynamodb:ReadDataForReplication, which authorizes replication, not
// a caller's read. GetRecords is authorized on the stream its iterator's
// prefix names.
func TestStreamsCallsNeedTheirActionOnTheStream(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		c := defaultCaller()
		arn, _ := latest(h.createStreamTable("Orders", "NEW_IMAGE"))
		h.put("Orders", "a")
		h.allRecords(arn)
		h.ok200(h.streamsCall("ListStreams", `{}`, c), &struct{}{})

		account, err := aws.ParseAccountID(c.Account)
		if err != nil {
			h.Fatal(err)
		}
		got := usesOf(h, access.Root{Account: account})
		for _, want := range []string{
			"dynamodb:ListStreams *",
			"dynamodb:DescribeStream " + arn,
			"dynamodb:GetShardIterator " + arn,
			"dynamodb:GetRecords " + arn,
		} {
			if !slices.Contains(got, want) {
				h.Errorf("recorded uses %q lack %q", got, want)
			}
		}
		for _, u := range got {
			if u == "dynamodb:ReadDataForReplication "+arn || u == "dynamodb:ReadDataForReplication *" {
				h.Errorf("recorded uses %q name dynamodb:ReadDataForReplication", got)
			}
		}
	})
}

// DescribeStream needs exactly dynamodb:DescribeStream on the stream: a
// principal whose only call is DescribeStream is recorded with that
// one action, so the policy written for it grants nothing more.
func TestDescribeStreamNeedsOnlyDescribeStream(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		c := defaultCaller()
		arn, _ := latest(h.createStreamTable("Orders", "NEW_IMAGE"))
		stream, err := aws.ParseARN(arn)
		if err != nil {
			h.Fatal(err)
		}
		scope, err := aws.ScopeOf(stream)
		if err != nil {
			h.Fatal(err)
		}
		role := aws.ARN{Partition: "aws", Service: "iam", Account: c.Account, Resource: "role/reader"}
		as := kernel.Target{Scope: scope, As: kernel.AsExecutionRole{Role: role}}
		if _, aerr := kernel.Invoke(h.Context(), h.Kernel, h.Call(dynamodbapi.OpDescribeTableRaw.Descriptor()), dynamodbstreamsapi.OpDescribeStream, as, &dynamodbstreamsapi.DescribeStreamInput{StreamArn: arn}); aerr != nil {
			h.Fatalf("DescribeStream: %v", aerr)
		}
		account, err := aws.ParseAccountID(c.Account)
		if err != nil {
			h.Fatal(err)
		}
		if got, want := usesOf(h, access.Role{Account: account, Name: "reader"}), []string{"dynamodb:DescribeStream " + arn}; !slices.Equal(got, want) {
			h.Errorf("DescribeStream's recorded uses = %q, want %q", got, want)
		}
	})
}

// A stream mapping's reads, made as the function's execution role
// (lambda-stream-sources.md 12.5), are recorded under the role with the
// stream as their resource, so GET /iam/policy for the role grants the
// three reads on the stream.
func TestStreamsReadsAsAnExecutionRoleAreRecordedUnderTheRole(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		c := defaultCaller()
		arn, _ := latest(h.createStreamTable("Orders", "NEW_IMAGE"))
		h.put("Orders", "a")
		stream, err := aws.ParseARN(arn)
		if err != nil {
			h.Fatal(err)
		}
		scope, err := aws.ScopeOf(stream)
		if err != nil {
			h.Fatal(err)
		}
		role := aws.ARN{Partition: "aws", Service: "iam", Account: c.Account, Resource: "role/service-role/worker-role"}
		as := kernel.Target{Scope: scope, As: kernel.AsExecutionRole{Role: role}}
		parent := h.Call(dynamodbapi.OpDescribeTableRaw.Descriptor())
		d, aerr := kernel.Invoke(h.Context(), h.Kernel, parent, dynamodbstreamsapi.OpDescribeStream, as, &dynamodbstreamsapi.DescribeStreamInput{StreamArn: arn})
		if aerr != nil {
			h.Fatalf("DescribeStream as the role: %v", aerr)
		}
		it, aerr := kernel.Invoke(h.Context(), h.Kernel, parent, dynamodbstreamsapi.OpGetShardIterator, as, &dynamodbstreamsapi.GetShardIteratorInput{
			StreamArn: arn, ShardId: *d.StreamDescription.Shards[0].ShardId, ShardIteratorType: dynamodbstreamsapi.ShardIteratorTypeTrimHorizon,
		})
		if aerr != nil {
			h.Fatalf("GetShardIterator as the role: %v", aerr)
		}
		if _, aerr := kernel.Invoke(h.Context(), h.Kernel, parent, dynamodbstreamsapi.OpGetRecords, as, &dynamodbstreamsapi.GetRecordsInput{ShardIterator: *it.ShardIterator}); aerr != nil {
			h.Fatalf("GetRecords as the role: %v", aerr)
		}

		account, err := aws.ParseAccountID(c.Account)
		if err != nil {
			h.Fatal(err)
		}
		got := usesOf(h, access.Role{Account: account, Path: "/service-role/", Name: "worker-role"})
		want := []string{"dynamodb:DescribeStream " + arn, "dynamodb:GetRecords " + arn, "dynamodb:GetShardIterator " + arn}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			h.Errorf("the role's recorded uses = %q, want %q", got, want)
		}
		doc, ok := h.Access.Policy(access.Role{Account: account, Path: "/service-role/", Name: "worker-role"})
		if !ok || len(doc.Statements) != 1 {
			h.Fatalf("Policy(role) = %+v, %v; want one statement granting the reads on the stream", doc, ok)
		}
	})
}
