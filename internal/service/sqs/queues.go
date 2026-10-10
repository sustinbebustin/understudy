package sqs

import (
	"context"
	"maps"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// bindQueues binds the queue control plane.
func (s *Service) bindQueues(b sqsapi.Binder) {
	b.CreateQueue(s.createQueue,
		registry.Partial{Note: "standard queues only: FIFO queues are refused; Policy, RedrivePolicy, RedriveAllowPolicy and the KMS attributes are checked for form and stored, not enforced"},
		kernel.WithValidator(validateCreateQueue),
		kernel.WithResources(func(c *kernel.Call, in *sqsapi.CreateQueueInput) []aws.ARN {
			return []aws.ARN{queueRef{scope: c.Scope(), name: in.QueueName}.arn()}
		}))
	b.GetQueueUrl(s.getQueueURL, registry.Full{},
		kernel.WithResources(func(c *kernel.Call, in *sqsapi.GetQueueUrlInput) []aws.ARN {
			q, err := namedQueue(c, in)
			if err != nil {
				return nil
			}
			return []aws.ARN{q.arn()}
		}))
}

// createQueue creates a standard queue, or returns the existing one's URL
// when every requested attribute equals its current value (sqs.md 5.1).
func (s *Service) createQueue(_ context.Context, c *kernel.Call, in *sqsapi.CreateQueueInput) (*sqsapi.CreateQueueOutput, *awserr.Error) {
	attrs := maps.Clone(in.Attributes)
	if fifo, ok := attrs[sqsapi.QueueAttributeNameFifoQueue]; ok {
		if strings.EqualFold(fifo, "true") {
			return nil, errFIFOUnsupported()
		}
		return nil, errUnknownAttribute(string(sqsapi.QueueAttributeNameFifoQueue))
	}
	settings, err := parseAttributes(attrs)
	if err != nil {
		return nil, err
	}
	q := queueRef{scope: c.Scope(), name: in.QueueName}
	_, err = kernel.Write(c, s.cells, q.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if existing, ok := st.Queues[q.name]; ok {
			if name, conflict := firstConflict(existing.Attributes, settings); conflict {
				return struct{}{}, errQueueExists(name)
			}
			tx.Unchanged()
			return struct{}{}, nil
		}
		st.Queues[q.name] = Queue{
			Name:       q.name,
			Created:    now,
			Modified:   now,
			Attributes: newAttributes(settings),
			Tags:       maps.Clone(in.Tags),
		}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.CreateQueueOutput{QueueUrl: new(queueURL(c, q))}, nil
}

// getQueueURL finds a queue by name, in another account when
// QueueOwnerAWSAccountId names one.
func (s *Service) getQueueURL(_ context.Context, c *kernel.Call, in *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
	q, err := namedQueue(c, in)
	if err != nil {
		return nil, err
	}
	if !s.exists(q) {
		return nil, errQueueDoesNotExist()
	}
	return &sqsapi.GetQueueUrlOutput{QueueUrl: new(queueURL(c, q))}, nil
}

// namedQueue is the queue GetQueueUrl names: QueueName in the account
// QueueOwnerAWSAccountId names, else the caller's.
func namedQueue(c *kernel.Call, in *sqsapi.GetQueueUrlInput) (queueRef, *awserr.Error) {
	var owner aws.AccountID
	if in.QueueOwnerAWSAccountId != nil {
		id, err := aws.ParseAccountID(*in.QueueOwnerAWSAccountId)
		if err != nil {
			return queueRef{}, errQueueDoesNotExist()
		}
		owner = id
	}
	scope, err := kernel.ResolveScope(c, kernel.CrossAccount{Account: owner})
	if err != nil {
		return queueRef{}, err
	}
	return queueRef{scope: scope, name: in.QueueName}, nil
}

// exists reports whether q exists, without creating a cell for its scope.
func (s *Service) exists(q queueRef) bool {
	cell, ok := s.cells.Lookup(q.scope)
	if !ok {
		return false
	}
	return store.Read(cell, func(st *State) bool {
		_, ok := st.Queues[q.name]
		return ok
	})
}
