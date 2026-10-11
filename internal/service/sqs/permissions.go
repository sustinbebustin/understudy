package sqs

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// maxPermissionActions is the most actions one AddPermission grants
// (sqs.md 5.3).
const maxPermissionActions = 7

// bindPermissions binds AddPermission and RemovePermission, which edit the
// statements of the stored Policy attribute; nothing enforces them.
func (s *Service) bindPermissions(b sqsapi.Binder) {
	stored := registry.StoredOnly{Note: "statements are stored in the queue's Policy attribute and not enforced"}
	b.AddPermission(s.addPermission, stored,
		kernel.WithCheck(checkAddPermission),
		queueByURL(func(in *sqsapi.AddPermissionInput) string { return in.QueueUrl }))
	b.RemovePermission(s.removePermission, stored,
		kernel.WithCheck(func(in *sqsapi.RemovePermissionInput) *awserr.Error { return checkLabel(in.Label) }),
		queueByURL(func(in *sqsapi.RemovePermissionInput) string { return in.QueueUrl }))
}

// checkLabel checks a permission label: 1 to 80 letters, digits, hyphens
// and underscores (sqs.md 5.3). RemovePermission checks it too, so an
// empty label never matches a statement without a Sid.
func checkLabel(label string) *awserr.Error {
	if !validBatchEntryID(label) {
		return errInvalidLabel(label)
	}
	return nil
}

// checkAddPermission checks the label, the account ids and the actions
// (sqs.md 5.3): an action is an SQS operation name or "*", at most seven.
func checkAddPermission(in *sqsapi.AddPermissionInput) *awserr.Error {
	if err := checkLabel(in.Label); err != nil {
		return err
	}
	for _, id := range in.AWSAccountIds {
		if _, err := aws.ParseAccountID(id); err != nil {
			return errInvalidAccountID(id)
		}
	}
	if len(in.Actions) > maxPermissionActions {
		return errTooManyActions(len(in.Actions))
	}
	for _, a := range in.Actions {
		if a == "*" {
			continue
		}
		op, ok := sqsapi.Service().Operation(a)
		if !ok || op.Kind != registry.Modeled {
			return errInvalidAction(a)
		}
	}
	return nil
}

// addPermission appends a statement named Label to the queue's Policy,
// creating the policy in the form AWS writes it when the queue has none
// (sqs.md 5.3). A label in use is refused, and so is a statement that
// would take the policy past the limits SetQueueAttributes enforces
// (parsePolicy), so every stored policy can be set back as read.
func (s *Service) addPermission(_ context.Context, c *kernel.Call, in *sqsapi.AddPermissionInput) (*sqsapi.AddPermissionOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	arn := q.arn().String()
	principals := make([]string, len(in.AWSAccountIds))
	for i, id := range in.AWSAccountIds {
		principals[i] = "arn:aws:iam::" + id + ":root"
	}
	actions := make([]string, len(in.Actions))
	for i, a := range in.Actions {
		actions[i] = "SQS:" + a
	}
	statement, jerr := json.Marshal(permission{
		Sid: in.Label, Effect: "Allow", Principal: principal{AWS: principals}, Action: actions, Resource: arn,
	})
	if jerr != nil {
		return nil, awserr.Internalf(jerr)
	}
	_, err = writeQueue(s, c, q, func(_ *State, queue *Queue, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		p, err := parseQueuePolicy(queue.Attributes.Policy, arn)
		if err != nil {
			return struct{}{}, err
		}
		if slices.Contains(p.sids(), in.Label) {
			return struct{}{}, errLabelExists(in.Label)
		}
		p.Statement = append(p.Statement, statement)
		if len(p.Statement) > maxPolicyStatements {
			return struct{}{}, errPermissionLimit("the policy would have " + strconv.Itoa(len(p.Statement)) + " statements; at most " + strconv.Itoa(maxPolicyStatements) + " are allowed.")
		}
		text, err := p.encode()
		if err != nil {
			return struct{}{}, err
		}
		if len(text) > maxPolicyBytes {
			return struct{}{}, errPermissionLimit("the policy would be " + strconv.Itoa(len(text)) + " bytes; at most " + strconv.Itoa(maxPolicyBytes) + " are allowed.")
		}
		queue.Attributes.Policy, queue.Modified = text, now
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.AddPermissionOutput{}, nil
}

// removePermission removes the statement named Label; removing the last
// statement removes the Policy attribute (sqs.md 5.3).
func (s *Service) removePermission(_ context.Context, c *kernel.Call, in *sqsapi.RemovePermissionInput) (*sqsapi.RemovePermissionOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	_, err = writeQueue(s, c, q, func(_ *State, queue *Queue, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		p, err := parseQueuePolicy(queue.Attributes.Policy, q.arn().String())
		if err != nil {
			return struct{}{}, err
		}
		i := slices.Index(p.sids(), in.Label)
		if i < 0 {
			return struct{}{}, errLabelNotFound(in.Label)
		}
		p.Statement = slices.Delete(p.Statement, i, i+1)
		text := ""
		if len(p.Statement) > 0 {
			if text, err = p.encode(); err != nil {
				return struct{}{}, err
			}
		}
		queue.Attributes.Policy, queue.Modified = text, now
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.RemovePermissionOutput{}, nil
}

// queuePolicy is a queue's Policy with its values kept verbatim, in the
// member order AWS writes: Version, Id, Statement.
type queuePolicy struct {
	Version   json.RawMessage   `json:"Version,omitempty"`
	ID        json.RawMessage   `json:"Id,omitempty"`
	Statement []json.RawMessage `json:"Statement"`
}

// parseQueuePolicy reads a stored Policy, which parsePolicy checked is a
// JSON object, or starts the one AddPermission creates for queue arn when
// there is none. Statement may be one object or a list.
func parseQueuePolicy(text, arn string) (queuePolicy, *awserr.Error) {
	if text == "" {
		return queuePolicy{Version: json.RawMessage(`"2008-10-17"`), ID: jsonx.AppendString(nil, arn+"/SQSDefaultPolicy")}, nil
	}
	var doc struct {
		Version   json.RawMessage `json:"Version"`
		ID        json.RawMessage `json:"Id"`
		Statement json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return queuePolicy{}, awserr.Internalf(err)
	}
	p := queuePolicy{Version: doc.Version, ID: doc.ID}
	switch raw := bytes.TrimSpace(doc.Statement); {
	case len(raw) == 0 || bytes.Equal(raw, []byte("null")):
	case raw[0] == '[':
		if err := json.Unmarshal(raw, &p.Statement); err != nil {
			return queuePolicy{}, awserr.Internalf(err)
		}
	default:
		p.Statement = []json.RawMessage{raw}
	}
	return p, nil
}

// sids returns each statement's Sid, "" for a statement without one.
func (p *queuePolicy) sids() []string {
	out := make([]string, len(p.Statement))
	for i, raw := range p.Statement {
		var st struct {
			Sid string `json:"Sid"`
		}
		if json.Unmarshal(raw, &st) == nil {
			out[i] = st.Sid
		}
	}
	return out
}

func (p *queuePolicy) encode() (string, *awserr.Error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", awserr.Internalf(err)
	}
	return string(b), nil
}

// permission is the statement AddPermission writes.
type permission struct {
	Sid       string    `json:"Sid"`
	Effect    string    `json:"Effect"`
	Principal principal `json:"Principal"`
	Action    oneOrMany `json:"Action"`
	Resource  string    `json:"Resource"`
}

type principal struct {
	AWS oneOrMany `json:"AWS"`
}

// oneOrMany is a policy value written as a string when it holds one item
// and as a list otherwise, as AWS writes Principal and Action.
type oneOrMany []string

// MarshalJSON writes the single item as a string.
func (v oneOrMany) MarshalJSON() ([]byte, error) {
	if len(v) == 1 {
		return json.Marshal(v[0])
	}
	return json.Marshal([]string(v))
}
