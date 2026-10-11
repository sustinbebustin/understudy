package dynamodb

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"net/http"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// answer says how Local's answer to one call is rewritten.
type answer struct {
	// arns is set for the table operations, whose answers carry table,
	// index and stream ARNs in Local's fixed account and region.
	arns bool
	// table is the table a ResourceNotFoundException names; empty for the
	// operations whose AWS text names none.
	table string
	// shim completes the table description of a successful table
	// operation; nil for the other operations.
	shim *shimmed
}

// Local mints every ARN in this account and region, whatever the
// credential (dynamodb-local.md 3).
const (
	localRegion  = "ddblocal"
	localAccount = "000000000000"
)

// localNotFound is Local's text for a missing table; AWS's names the table
// on the table operations only (dynalite db/index.js L486-L520, recorded
// against real DynamoDB; dynamodb-local.md 4.2).
const localNotFound = "Cannot do operations on a non-existent table"

// awsNotFound is AWS's text for a missing table, which the Terraform
// provider matches on DeleteTable (dynamodb-local.md 7.1).
func awsNotFound(table string) string {
	if table == "" {
		return "Requested resource not found"
	}
	return "Requested resource not found: Table: " + table + " not found"
}

// rewrite returns Local's answer as the client receives it. A table
// operation's ARNs are moved into the caller's account and region; an
// error's message moves under the shape's own member spelling (Local sends
// Message everywhere, which the Go SDK drops on a message-shaped error) and
// Local's texts that clients match are translated. A changed body is
// re-encoded through a document tree, keeping member order and number text,
// with x-amz-crc32 recomputed; any other body passes through byte for byte
// with Local's checksum. Every answer carries understudy's request id, the
// one the journal records, and an x-amz-crc32 header, as AWS sends on
// errors too.
func rewrite(c *kernel.Call, a answer, local kernel.RawResponse) (kernel.RawResponse, *awserr.Error) {
	body := local.Body
	if changed, ok := rewritten(c, a, local); ok {
		if err := checkUTF8(changed); err != nil {
			return kernel.RawResponse{}, awserr.Internalf(fmt.Errorf("rewriting DynamoDB Local's answer to %s: %w", c.OperationID(), err))
		}
		enc := jsonx.NewEncoder(jsonx.WireOrder)
		enc.Value(changed)
		b, err := enc.Bytes()
		if err != nil {
			return kernel.RawResponse{}, awserr.Internalf(fmt.Errorf("re-encoding DynamoDB Local's answer to %s: %w", c.OperationID(), err))
		}
		body = b
	}
	header := http.Header{
		"Content-Type":     {local.Header.Get("Content-Type")},
		"Content-Length":   {strconv.Itoa(len(body))},
		"X-Amzn-Requestid": {string(c.ID())},
	}
	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", jsonMediaType)
	}
	sum := local.Header.Get("X-Amz-Crc32")
	if sum == "" || !bytes.Equal(body, local.Body) {
		sum = strconv.FormatUint(uint64(crc32.ChecksumIEEE(body)), 10)
	}
	header.Set("X-Amz-Crc32", sum)
	return kernel.RawResponse{Status: local.Status, Header: header, Body: body}, nil
}

// rewritten returns the rewritten document of Local's answer, and false
// when the answer is to pass through: it needs no rewrite, or it is not
// JSON (Local's 500 pages), which only Local can explain.
func rewritten(c *kernel.Call, a answer, local kernel.RawResponse) (document.Value, bool) {
	if local.Status == http.StatusOK && !a.arns {
		return nil, false
	}
	dec := jsonx.NewDecoder(local.Body)
	doc, err := dec.ReadValue()
	if err != nil || dec.End() != nil {
		return nil, false
	}
	m, ok := doc.(document.Map)
	if !ok {
		return nil, false
	}
	if local.Status == http.StatusOK {
		changed := rewriteARNs(m, c.Scope())
		if a.shim != nil {
			changed = a.shim.complete(m) || changed
		}
		return m, changed
	}
	return m, fixError(m, c.Op(), a)
}

// rewriteARNs moves every ARN Local minted under an ARN member of v into
// scope, and reports whether it changed one. Only members that hold ARNs
// are read, never item data.
func rewriteARNs(v document.Value, scope aws.Scope) bool {
	changed := false
	switch v := v.(type) {
	case document.Map:
		for i, e := range v {
			if s, ok := e.Value.(document.String); ok && arnMember(e.Key) {
				if moved, ok := scoped(string(s), scope); ok {
					v[i].Value = document.String(moved)
					changed = true
				}
				continue
			}
			changed = rewriteARNs(e.Value, scope) || changed
		}
	case document.List:
		for _, x := range v {
			changed = rewriteARNs(x, scope) || changed
		}
	case document.Null, document.Bool, document.Number, document.String:
	}
	return changed
}

// arnMember reports whether a table description member holds an ARN.
func arnMember(name string) bool {
	switch name {
	case "TableArn", "IndexArn", "LatestStreamArn", "StreamArn":
		return true
	}
	return false
}

// scoped returns arn in scope when Local minted it.
func scoped(arn string, scope aws.Scope) (string, bool) {
	parsed, err := aws.ParseARN(arn)
	if err != nil || parsed.Region != localRegion || parsed.Account != localAccount {
		return "", false
	}
	parsed.Partition, parsed.Region, parsed.Account = scope.Partition.String(), scope.Region.String(), scope.Account.String()
	return parsed.String(), true
}

// fixError puts an error body's message under the member spelling of its
// __type's shape and translates Local's texts, reporting whether it changed
// anything. A __type the operation does not send stays as Local wrote it.
func fixError(m document.Map, op *registry.Operation, a answer) bool {
	id := identityOf(m, op)
	if id == nil {
		return false
	}
	at := slices.IndexFunc(m, func(e document.Entry) bool { return e.Key == "message" || e.Key == "Message" })
	if at < 0 {
		return false
	}
	changed := false
	if text, ok := m[at].Value.(document.String); ok && text == localNotFound && id.Shape == dynamodbapi.IdentityResourceNotFoundException().Shape {
		m[at].Value = document.String(awsNotFound(a.table))
		changed = true
	}
	switch id.MessageKey {
	case awserr.MessageLower:
		changed = changed || m[at].Key != "message"
		m[at].Key = "message"
	case awserr.MessageUpper:
		changed = changed || m[at].Key != "Message"
		m[at].Key = "Message"
	case awserr.NoMessage:
	}
	return changed
}

// identityOf finds the identity whose __type an error body carries among
// the errors op sends and the service's generic ones, or nil when it
// carries none of them.
func identityOf(m document.Map, op *registry.Operation) *awserr.Identity {
	at := slices.IndexFunc(m, func(e document.Entry) bool { return e.Key == "__type" })
	if at < 0 {
		return nil
	}
	typ, ok := m[at].Value.(document.String)
	if !ok {
		return nil
	}
	match := func(id awserr.Identity) bool { return id.JSONType == string(typ) }
	if i := slices.IndexFunc(op.Errors, match); i >= 0 {
		return &op.Errors[i]
	}
	generic := dynamodbapi.GenericErrors(awserr.JSONFamily)
	if i := slices.IndexFunc(generic, match); i >= 0 {
		return &generic[i]
	}
	return nil
}

// InvalidUTF8Error is a Local answer whose strings re-encoding would
// change: jsonx writes an invalid byte as U+FFFD, so the client would read
// a different string than Local sent.
type InvalidUTF8Error struct{ Value string }

func (e *InvalidUTF8Error) Error() string {
	return fmt.Sprintf("the answer holds the string %q, which is not valid UTF-8 and would change if re-encoded", e.Value)
}

// checkUTF8 refuses a document holding a string or member name that is
// not valid UTF-8.
func checkUTF8(v document.Value) error {
	switch v := v.(type) {
	case document.String:
		if !utf8.ValidString(string(v)) {
			return &InvalidUTF8Error{Value: string(v)}
		}
	case document.Map:
		for _, e := range v {
			if !utf8.ValidString(e.Key) {
				return &InvalidUTF8Error{Value: e.Key}
			}
			if err := checkUTF8(e.Value); err != nil {
				return err
			}
		}
	case document.List:
		for _, x := range v {
			if err := checkUTF8(x); err != nil {
				return err
			}
		}
	case document.Null, document.Bool, document.Number:
	}
	return nil
}
