package dynamodb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// invoker makes the TTL sweep's calls to Local through the pipeline, as
// children of the timer's call, so the journal, fault rules and access
// recorder see them like any call.
type invoker struct {
	s      *Service
	parent *kernel.Call
	target kernel.Target
}

func (x invoker) describe(ctx context.Context, table string, out *dynamodbapi.DescribeTableOutput) error {
	resp, err := kernel.Invoke(ctx, x.s.kernel, x.parent, dynamodbapi.OpDescribeTableRaw, x.target, &dynamodbapi.DescribeTableInput{TableName: table})
	return result(dynamodbapi.OpDescribeTableRaw.Descriptor(), resp, err, out)
}

// scan runs in page by page, following LastEvaluatedKey, and hands each
// item to each; an error from each ends the scan.
func (x invoker) scan(ctx context.Context, in *dynamodbapi.ScanInput, each func(item map[string]dynamodbapi.AttributeValue) error) error {
	for {
		var out dynamodbapi.ScanOutput
		resp, aerr := kernel.Invoke(ctx, x.s.kernel, x.parent, dynamodbapi.OpScanRaw, x.target, in)
		if err := result(dynamodbapi.OpScanRaw.Descriptor(), resp, aerr, &out); err != nil {
			return err
		}
		for _, item := range out.Items {
			if err := each(item); err != nil {
				return err
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			return nil
		}
		next := *in
		next.ExclusiveStartKey = out.LastEvaluatedKey
		in = &next
	}
}

// deleteExpired deletes the item key of table if its attribute still lies
// in the window, and reports whether it did: an item refreshed since the
// scan fails the condition and stays.
func (x invoker) deleteExpired(ctx context.Context, table, attribute string, key map[string]dynamodbapi.AttributeValue, expired window) (bool, error) {
	in := &dynamodbapi.DeleteItemInput{
		TableName: table, Key: key,
		ConditionExpression:       new(expiredCondition),
		ExpressionAttributeNames:  map[string]string{"#ttl": attribute},
		ExpressionAttributeValues: expired.values(),
	}
	resp, aerr := kernel.Invoke(ctx, x.s.kernel, x.parent, dynamodbapi.OpDeleteItemRaw, x.target, in)
	err := result(dynamodbapi.OpDeleteItemRaw.Descriptor(), resp, aerr, nil)
	if le, ok := errors.AsType[*LocalError](err); ok && le.is("ConditionalCheckFailedException") {
		return false, nil
	}
	return err == nil, err
}

// result reads the answer to a call the shims made into out (nil to
// ignore its body): understudy's refusal as its BoundaryError, Local's as a
// *LocalError, and a body that does not decode as an *AnswerError.
func result(op *registry.Operation, resp *kernel.RawOutput, aerr *awserr.Error, out smithy.Deserializable) error {
	if aerr != nil {
		return aerr.AsError()
	}
	return decodeAnswer(op, resp.Response, out)
}

// decodeAnswer reads one of Local's answers to op into out (nil to ignore
// its body); an error answer is a *LocalError.
func decodeAnswer(op *registry.Operation, resp kernel.RawResponse, out smithy.Deserializable) error {
	if resp.Status != http.StatusOK {
		return localError(op, resp)
	}
	if out == nil {
		return nil
	}
	r := &http.Request{Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(resp.Body))}
	if err := (awsjson.Codec{Version: awsjson.V10}).Decode(r, op, out); err != nil {
		return &AnswerError{Op: op.ID, Reason: err.Message}
	}
	return nil
}

// LocalError is DynamoDB Local refusing a call the shims made.
type LocalError struct {
	Op     ref.OperationID
	Status int
	// Type is the answer's __type, such as
	// com.amazonaws.dynamodb.v20120810#ResourceNotFoundException.
	Type    string
	Message string
}

func (e *LocalError) Error() string {
	return fmt.Sprintf("DynamoDB Local refused %s with %d %s: %s", e.Op, e.Status, e.Type, e.Message)
}

// is reports whether the error is the shape named name, in any namespace.
func (e *LocalError) is(name string) bool {
	_, short, _ := strings.Cut(e.Type, "#")
	return short == name
}

func localError(op *registry.Operation, resp kernel.RawResponse) *LocalError {
	e := &LocalError{Op: op.ID, Status: resp.Status, Message: string(resp.Body)}
	dec := jsonx.NewDecoder(resp.Body)
	doc, err := dec.ReadValue()
	if m, ok := doc.(document.Map); ok && err == nil {
		if t, ok := member(m, "__type").(document.String); ok {
			e.Type = string(t)
		}
		for _, key := range []string{"message", "Message"} {
			if text, ok := member(m, key).(document.String); ok {
				e.Message = string(text)
			}
		}
	}
	return e
}

// AnswerError is an answer from Local the shims cannot read.
type AnswerError struct {
	Op     ref.OperationID
	Reason string
}

func (e *AnswerError) Error() string {
	return fmt.Sprintf("DynamoDB Local's answer to %s cannot be read: %s", e.Op, e.Reason)
}
