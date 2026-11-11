package ssm

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/serviceevent"
)

// ssmPrincipal is the service principal Parameter Store Change events are
// put as.
const ssmPrincipal = "ssm.amazonaws.com"

// The Parameter Store Change envelope (ssm-parameters.md 9.2; SSM-31).
const (
	eventSource     = "aws.ssm"
	eventDetailType = "Parameter Store Change"
)

// changeOp is a Parameter Store Change event's detail.operation
// (ssm-parameters.md 9.2; SSM-31).
type changeOp string

const (
	opCreate changeOp = "Create"
	// opUpdate is an overwrite.
	opUpdate changeOp = "Update"
	// opLabel is a label attached or moved.
	opLabel  changeOp = "LabelParameterVersion"
	opDelete changeOp = "Delete"
)

// labelChange is the label a LabelParameterVersion event reports: the
// version it left, nil for a first label, and the version it now names.
type labelChange struct {
	label string
	from  *int64
	to    int64
}

// changeEvent is the Parameter Store Change event of parameter p in scope
// after (or, for a delete, before) a committed change: name, description,
// type and operation on every event, label, fromVersion and toVersion as
// strings on a label event (ssm-parameters.md 9.2; SSM-31). An overwrite
// carries the description the version kept. A parameter with no
// description sends "" (inferred: SSM-31 recorded one with a
// description).
func changeEvent(scope aws.Scope, p *Parameter, op changeOp, l *labelChange) serviceevent.Event {
	v := p.latest()
	description := ""
	if v.Description != nil {
		description = *v.Description
	}
	b := []byte(`{"name":`)
	b = jsonx.AppendString(b, p.Name)
	b = append(b, `,"description":`...)
	b = jsonx.AppendString(b, description)
	b = append(b, `,"type":`...)
	b = jsonx.AppendString(b, string(v.Type))
	b = append(b, `,"operation":`...)
	b = jsonx.AppendString(b, string(op))
	if l != nil {
		from := ""
		if l.from != nil {
			from = strconv.FormatInt(*l.from, 10)
		}
		b = append(b, `,"label":`...)
		b = jsonx.AppendString(b, l.label)
		b = append(b, `,"fromVersion":`...)
		b = jsonx.AppendString(b, from)
		b = append(b, `,"toVersion":`...)
		b = jsonx.AppendString(b, strconv.FormatInt(l.to, 10))
	}
	b = append(b, '}')
	return serviceevent.Event{
		Scope: scope, Principal: ssmPrincipal, Via: ref.SSMParameter,
		Source: eventSource, DetailType: eventDetailType, Detail: string(b),
		Resources: []string{parameterARN(scope, p.Name).String()},
	}
}

// lostEvent warns of a Parameter Store Change event that did not reach the
// default bus, for a kernel without EventBridge or a failed send; it is
// not retried.
func (s *Service) lostEvent(ctx context.Context, e *serviceevent.Event, why string) {
	s.k.Log().LogAttrs(ctx, slog.LevelWarn, "a Parameter Store Change event did not reach the default event bus and is not sent again; the parameter change it reports stands",
		slog.String("parameter", strings.Join(e.Resources, ",")), slog.String("error", why))
}
