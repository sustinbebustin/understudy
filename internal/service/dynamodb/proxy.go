package dynamodb

import (
	"context"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/store"
)

// forward sends one call naming the tables addressed to Local and returns
// its rewritten answer: the pre-forward checks, the scope and exact-name
// checks of the addressed tables, Local's start, the body (the client's
// bytes or the encoded input), the rewritten credential, then the answer
// rewrite. Local's own error answers come back as raw responses, written to
// the client as rewritten.
func (s *Service) forward(ctx context.Context, c *kernel.Call, in smithy.Serializable, w kernel.Wire, a answer, addressed []string) (kernel.RawResponse, *awserr.Error) {
	if err := preForward(c); err != nil {
		return kernel.RawResponse{}, err
	}
	if err := inScope(c.Scope(), addressed, a); err != nil {
		return kernel.RawResponse{}, err
	}
	if err := s.resolveExactly(c.Scope(), addressed, a); err != nil {
		return kernel.RawResponse{}, err
	}
	body, err := requestBody(c.Op(), in, w)
	if err != nil {
		return kernel.RawResponse{}, err
	}
	endpoint, err := s.ready(ctx)
	if err != nil {
		return kernel.RawResponse{}, err
	}
	local, err := s.send(ctx, endpoint, exchangeOf(c), body)
	if err != nil {
		return kernel.RawResponse{}, err
	}
	return rewrite(c, a, local)
}

// serializable is the pointer to a generated input, which encodes itself.
type serializable[In any] interface {
	*In
	smithy.Serializable
}

// passthrough is the handler of an operation Local serves as AWS does once
// the tables it names resolve exactly: its answer passes through,
// rewritten as a says.
func passthrough[In any, P serializable[In]](s *Service, names func(*In) []string, a func(*In) answer) kernel.RawHandler[In] {
	return func(ctx context.Context, c *kernel.Call, in *In, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
		return s.forward(ctx, c, P(in), w, a(in), names(in))
	}
}

// noTables is the names of an operation that addresses no table by a
// member, so there is nothing to resolve before Local sees it.
func noTables[In any](*In) []string { return nil }

// inScope refuses a call naming a table by the ARN of another partition,
// account or region than the caller's. Local ignores the ARN's account and
// region (dynamodb-local.md 4.2), so it would act on the caller's own table
// of that name. AWS never serves another region's table from this region's
// endpoint, and serves another account's table only through a resource
// policy, which understudy does not hold. That AWS answers
// ResourceNotFoundException with this text is UNVERIFIED. Only a
// well-formed table ARN is checked: any other text, an ARN of another
// service or one without table/ included, is Local's to refuse with its
// own ValidationException, whatever region it names.
func inScope(scope aws.Scope, names []string, a answer) *awserr.Error {
	for _, n := range names {
		arn, err := aws.ParseARN(n)
		if err != nil || arn.Service != "dynamodb" || !strings.HasPrefix(arn.Resource, "table/") {
			continue
		}
		if arn.Partition != scope.Partition.String() || arn.Region != scope.Region.String() || arn.Account != scope.Account.String() {
			return dynamodbapi.ErrResourceNotFoundException(awsNotFound(a.table))
		}
	}
	return nil
}

// resolveExactly refuses a call naming a table the scope does not hold
// while it holds a case twin of it: AWS answers ResourceNotFoundException,
// and Local, which resolves names case-insensitively, would act on the
// twin (dynamodb-local.md 4.1). A name the registry knows nothing about is
// Local's to answer.
func (s *Service) resolveExactly(scope aws.Scope, names []string, a answer) *awserr.Error {
	if len(names) == 0 {
		return nil
	}
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	missing := store.Read(cell, func(st *State) bool {
		return slices.ContainsFunc(names, func(n string) bool { return st.onlyTwinOf(tableName(n)) })
	})
	if missing {
		return dynamodbapi.ErrResourceNotFoundException(awsNotFound(a.table))
	}
	return nil
}

// plainAnswer is the answer of an operation whose output holds no table
// ARNs or names and whose not-found text names no table: passed through
// untouched on success.
func plainAnswer[In any](*In) answer { return answer{} }

// tableAnswer is the answer of a table operation naming table: its ARNs
// are rewritten and its not-found text names the table.
func tableAnswer(table string) answer { return answer{arns: true, table: table} }

// tables names the resources a call addresses, so request-phase fault
// rules match them by table ARN (ARCHITECTURE.md 11).
func tables[In any](names func(*In) []string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		out := make([]aws.ARN, 0, 1)
		for _, n := range names(in) {
			out = append(out, tableARN(c.Scope(), n))
		}
		return out
	})
}

// tableARN is the ARN of the table name addresses in scope: name may itself
// be a table ARN, which DynamoDB accepts wherever a table name goes.
func tableARN(scope aws.Scope, name string) aws.ARN {
	if arn, err := aws.ParseARN(name); err == nil {
		return arn
	}
	return aws.ARN{
		Partition: scope.Partition.String(), Service: "dynamodb",
		Region: scope.Region.String(), Account: scope.Account.String(),
		Resource: "table/" + name,
	}
}

// tableName is the name a TableName member addresses: itself, or the name
// inside a table ARN.
func tableName(name string) string {
	if arn, err := aws.ParseARN(name); err == nil {
		if rest, ok := strings.CutPrefix(arn.Resource, "table/"); ok {
			return rest
		}
	}
	return name
}
