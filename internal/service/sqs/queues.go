package sqs

import (
	"context"
	"encoding/base64"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

const (
	// deletedWait is how long AWS refuses a deleted queue's name.
	deletedWait = 60 * time.Second
	// purgeWait is how long AWS refuses a second PurgeQueue.
	purgeWait = 60 * time.Second
	// maxTags is the tag count above which TagQueue refuses (sqs.md 5.3).
	maxTags = 50
	// maxListResults is ListQueues' and ListDeadLetterSourceQueues' page
	// size without MaxResults, and its cap.
	maxListResults = 1000
)

// bindQueues binds the queue control plane.
func (s *Service) bindQueues(b sqsapi.Binder) {
	b.CreateQueue(s.createQueue, registry.Full{},
		kernel.WithValidator(validateCreateQueue),
		kernel.WithCheck(func(in *sqsapi.CreateQueueInput) *awserr.Error { return checkTags(in.Tags) }),
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
	b.DeleteQueue(s.deleteQueue, registry.Full{},
		queueByURL(func(in *sqsapi.DeleteQueueInput) string { return in.QueueUrl }))
	b.ListQueues(s.listQueues, registry.Full{},
		kernel.WithCheck(func(in *sqsapi.ListQueuesInput) *awserr.Error { return checkMaxResults(in.MaxResults) }))
	b.GetQueueAttributes(s.getQueueAttributes, registry.Full{},
		queueByURL(func(in *sqsapi.GetQueueAttributesInput) string { return in.QueueUrl }))
	b.SetQueueAttributes(s.setQueueAttributes, registry.Full{},
		queueByURL(func(in *sqsapi.SetQueueAttributesInput) string { return in.QueueUrl }))
	b.PurgeQueue(s.purgeQueue, registry.Full{},
		queueByURL(func(in *sqsapi.PurgeQueueInput) string { return in.QueueUrl }))
	b.TagQueue(s.tagQueue, registry.Full{},
		kernel.WithCheck(func(in *sqsapi.TagQueueInput) *awserr.Error { return checkTags(in.Tags) }),
		queueByURL(func(in *sqsapi.TagQueueInput) string { return in.QueueUrl }))
	b.UntagQueue(s.untagQueue, registry.Full{},
		kernel.WithCheck(checkUntagQueue),
		queueByURL(func(in *sqsapi.UntagQueueInput) string { return in.QueueUrl }))
	b.ListQueueTags(s.listQueueTags, registry.Full{},
		queueByURL(func(in *sqsapi.ListQueueTagsInput) string { return in.QueueUrl }))
	b.ListDeadLetterSourceQueues(s.listDeadLetterSourceQueues, registry.Full{},
		kernel.WithCheck(func(in *sqsapi.ListDeadLetterSourceQueuesInput) *awserr.Error { return checkMaxResults(in.MaxResults) }),
		queueByURL(func(in *sqsapi.ListDeadLetterSourceQueuesInput) string { return in.QueueUrl }))
}

// checkMaxResults checks a listing's page size, 1 to 1,000 (sqs.md 5.2).
func checkMaxResults(n *int32) *awserr.Error {
	if n != nil && (*n < 1 || *n > maxListResults) {
		return errMaxResults(*n, maxListResults)
	}
	return nil
}

// createQueue creates a queue, or returns the existing one's URL when
// every requested attribute equals its current value (sqs.md 5.1).
// validateCreateQueue has checked that the name's .fifo suffix and
// FifoQueue agree.
func (s *Service) createQueue(_ context.Context, c *kernel.Call, in *sqsapi.CreateQueueInput) (*sqsapi.CreateQueueOutput, *awserr.Error) {
	fifo := strings.HasSuffix(in.QueueName, ".fifo")
	settings, err := parseAttributes(in.Attributes, fifo)
	if err != nil {
		return nil, err
	}
	if len(in.Tags) > maxTags {
		return nil, errTooManyTags(in.QueueName)
	}
	q := queueRef{scope: c.Scope(), name: in.QueueName}
	_, err = kernel.Write(c, s.cells, q.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if existing, ok := st.Queues[q.name]; ok {
			if name, conflict := firstConflict(&existing, settings); conflict {
				return struct{}{}, errQueueExists(name)
			}
			tx.Unchanged()
			return struct{}{}, nil
		}
		if at, ok := st.Deleted[q.name]; ok && s.opts.DeletedRecently == DeletedRecentlyEnforce && now.Before(at.Add(deletedWait)) {
			return struct{}{}, errQueueDeletedRecently()
		}
		queue := newQueue(q.name, fifo, in.Tags, settings, now)
		if err := checkThroughput(&queue); err != nil {
			return struct{}{}, err
		}
		if err := checkRedrive(st, q.scope, &queue); err != nil {
			return struct{}{}, err
		}
		st.Queues[q.name] = queue
		pruneDeleted(st, q.name, now)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.CreateQueueOutput{QueueUrl: new(queueURL(c, q))}, nil
}

// newQueue is a queue created at now with the default attributes, then
// settings; a FIFO queue's deduplication scope is fixed from here on.
func newQueue(name string, fifo bool, tags map[string]string, settings []setting, now clock.Instant) Queue {
	queue := Queue{Name: name, Created: now, Modified: now, Attributes: defaultAttributes(), Tags: maps.Clone(tags)}
	if fifo {
		queue.FIFO = &FIFO{DeduplicationScope: DedupQueue, FifoThroughputLimit: ThroughputPerQueue}
	}
	for _, set := range settings {
		set.apply(&queue)
	}
	if queue.FIFO != nil {
		queue.FIFO.CreatedScope = queue.FIFO.DeduplicationScope
	}
	return queue
}

// pruneDeleted drops the tombstone of name and every tombstone past the
// wait.
func pruneDeleted(st *State, name string, now clock.Instant) {
	for n, at := range st.Deleted {
		if n == name || !now.Before(at.Add(deletedWait)) {
			delete(st.Deleted, n)
		}
	}
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

// deleteQueue deletes a queue and its messages at once (sqs.md 5.2). Long
// polls on it wake and answer NonExistentQueue; a move task into it fails
// with that code (sqs.md 9.3) and the tasks out of it go with it.
func (s *Service) deleteQueue(_ context.Context, c *kernel.Call, in *sqsapi.DeleteQueueInput) (*sqsapi.DeleteQueueOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	// Not writeQueue, which would store the deleted queue back.
	_, err = kernel.Write(c, s.cells, q.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		queue, ok := st.Queues[q.name]
		if !ok {
			return struct{}{}, errQueueDoesNotExist()
		}
		for i := range queue.Messages {
			s.disarmMessage(tx, q, queue.Messages[i].ID)
		}
		delete(st.Queues, q.name)
		if s.opts.DeletedRecently == DeletedRecentlyEnforce {
			if st.Deleted == nil {
				st.Deleted = map[string]clock.Instant{}
			}
			st.Deleted[q.name] = now
		}
		s.dropTasks(tx, st, q)
		tx.Wake(q.waitKey())
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.DeleteQueueOutput{}, nil
}

// dropTasks settles the move tasks of the deleted queue q: the tasks out
// of it go with it, and a running task into it fails with
// NonExistentQueue.
func (s *Service) dropTasks(tx *kernel.Tx, st *State, q queueRef) {
	st.Tasks = slices.DeleteFunc(st.Tasks, func(t MoveTask) bool {
		if t.Source == q && t.running() {
			tx.Disarm(s.moveTask, t.timerID())
		}
		return t.Source == q
	})
	for i := range st.Tasks {
		if t := st.Tasks[i]; t.Target == (ToQueue{Queue: q}) && t.running() {
			s.endTask(tx, &t, TaskFailed{Reason: sqsapi.IdentityQueueDoesNotExist().QueryCode})
			st.Tasks[i] = t
		}
	}
}

// listQueues lists the caller's queues in its region by name, with a name
// prefix and pages (sqs.md 5.2).
func (s *Service) listQueues(_ context.Context, c *kernel.Call, in *sqsapi.ListQueuesInput) (*sqsapi.ListQueuesOutput, *awserr.Error) {
	scope := c.Scope()
	var names []string
	if cell, ok := s.cells.Lookup(scope); ok {
		names = store.Read(cell, func(st *State) []string {
			out := make([]string, 0, len(st.Queues))
			for name := range st.Queues {
				if in.QueueNamePrefix == nil || strings.HasPrefix(name, *in.QueueNamePrefix) {
					out = append(out, name)
				}
			}
			return out
		})
	}
	page, next, err := paginate(names, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	return &sqsapi.ListQueuesOutput{QueueUrls: urlsOf(c, scope, page), NextToken: next}, nil
}

func urlsOf(c *kernel.Call, scope aws.Scope, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = queueURL(c, queueRef{scope: scope, name: name})
	}
	return out
}

// paginate sorts names and returns the page after token, of max names
// (default and cap 1,000), with the token of the next page. A token comes
// back only when the caller set MaxResults and more names follow
// (sqs.md 5.2). The token is the last name of the page, encoded.
func paginate(names []string, maxResults *int32, token *string) (page []string, next *string, err *awserr.Error) {
	slices.Sort(names)
	if token != nil {
		after, derr := base64.RawURLEncoding.DecodeString(*token)
		if derr != nil || len(after) == 0 {
			return nil, nil, errNextToken()
		}
		i, _ := slices.BinarySearch(names, string(after))
		for i < len(names) && names[i] <= string(after) {
			i++
		}
		names = names[i:]
	}
	size := maxListResults
	if maxResults != nil {
		size = int(*maxResults)
	}
	if len(names) <= size {
		return names, nil, nil
	}
	page = names[:size]
	if maxResults == nil {
		return page, nil, nil
	}
	return page, new(base64.RawURLEncoding.EncodeToString([]byte(page[len(page)-1]))), nil
}

// getQueueAttributes returns the named attributes, every one for All, none
// when no name is given (sqs.md 4). A known name that does not apply to
// the queue is left out; an unknown one is InvalidAttributeName.
func (s *Service) getQueueAttributes(_ context.Context, c *kernel.Call, in *sqsapi.GetQueueAttributesInput) (*sqsapi.GetQueueAttributesOutput, *awserr.Error) {
	q, err := resolveURL(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	now := s.k.Clock().Now()
	all, err := readQueue(s, q, func(_ *State, queue *Queue) map[sqsapi.QueueAttributeName]string {
		return report(queue, q.arn(), now)
	})
	if err != nil {
		return nil, err
	}
	out := map[sqsapi.QueueAttributeName]string{}
	for _, name := range in.AttributeNames {
		switch {
		case name == sqsapi.QueueAttributeNameAll:
			maps.Copy(out, all)
		case !name.Known():
			return nil, errUnknownAttribute(string(name))
		default:
			if v, ok := all[name]; ok {
				out[name] = v
			}
		}
	}
	if len(out) == 0 {
		return &sqsapi.GetQueueAttributesOutput{}, nil
	}
	return &sqsapi.GetQueueAttributesOutput{Attributes: out}, nil
}

// setQueueAttributes changes the given attributes at once (sqs.md 4):
// "" clears a policy or the KMS key; a lower MessageRetentionPeriod
// expires older messages; DelaySeconds is retroactive on FIFO queues only.
func (s *Service) setQueueAttributes(_ context.Context, c *kernel.Call, in *sqsapi.SetQueueAttributesInput) (*sqsapi.SetQueueAttributesOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	_, err = writeQueue(s, c, q, func(st *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		settings, err := parseAttributes(in.Attributes, queue.FIFO != nil)
		if err != nil {
			return struct{}{}, err
		}
		// queue is writeQueue's working copy, so a check failing after the
		// settings applied leaves the stored queue as it was.
		old := queue.Attributes
		for _, set := range settings {
			set.apply(queue)
		}
		if err := checkThroughput(queue); err != nil {
			return struct{}{}, err
		}
		if sets(settings, sqsapi.QueueAttributeNameRedrivePolicy) {
			if err := checkRedrive(st, q.scope, queue); err != nil {
				return struct{}{}, err
			}
		}
		queue.Modified = now
		s.retime(tx, q, old, queue, now)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.SetQueueAttributesOutput{}, nil
}

// retime re-arms queue's message timers after its attributes changed from
// old: every retention timer when MessageRetentionPeriod changed, and on a
// FIFO queue whose DelaySeconds changed the visibility of every message
// not received yet, as the delay is retroactive there (sqs.md 4).
func (s *Service) retime(tx *kernel.Tx, q queueRef, old Attributes, queue *Queue, now clock.Instant) {
	retention := queue.Attributes.MessageRetentionPeriod != old.MessageRetentionPeriod
	retroactive := queue.FIFO != nil && queue.Attributes.DelaySeconds != old.DelaySeconds
	if !retention && !retroactive {
		return
	}
	for i := range queue.Messages {
		if retroactive && queue.Messages[i].Receives == nil {
			m := queue.message(i)
			m.VisibleAt = m.Sent.Add(seconds(queue.Attributes.DelaySeconds))
			s.armVisibility(tx, q, m, now)
		}
		if retention {
			s.armRetention(tx, q, queue, &queue.Messages[i])
		}
	}
}

// purgeQueue deletes every message, delayed and in flight ones included,
// and clears the FIFO deduplication cache, once per 60 seconds (sqs.md
// 5.3).
func (s *Service) purgeQueue(_ context.Context, c *kernel.Call, in *sqsapi.PurgeQueueInput) (*sqsapi.PurgeQueueOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	_, err = writeQueue(s, c, q, func(_ *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if !queue.Purged.IsZero() && now.Before(queue.Purged.Add(purgeWait)) {
			return struct{}{}, errPurgeInProgress(queue.Name)
		}
		for i := range queue.Messages {
			s.disarmMessage(tx, q, queue.Messages[i].ID)
		}
		queue.Messages = nil
		if queue.FIFO != nil {
			queue.FIFO.Dedup = DedupCache{}
			queue.FIFO.Attempts = nil
		}
		queue.Purged = now
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.PurgeQueueOutput{}, nil
}

// checkTags checks the tags of CreateQueue and TagQueue against the rules
// of sqs.md 5.3, keys in order; the count, which TagQueue counts with the
// queue's own tags, is checked by the handlers.
func checkTags(tags map[string]string) *awserr.Error {
	for _, key := range slices.Sorted(maps.Keys(tags)) {
		if err := checkTagKey(key); err != nil {
			return err
		}
		value := tags[key]
		if utf8.RuneCountInString(value) > 256 {
			return errTagValueLength(value)
		}
		if !validTagText(value) {
			return errTagCharacters("TagValue", value)
		}
	}
	return nil
}

// checkUntagQueue checks the keys UntagQueue removes as tag keys.
func checkUntagQueue(in *sqsapi.UntagQueueInput) *awserr.Error {
	for _, key := range in.TagKeys {
		if err := checkTagKey(key); err != nil {
			return err
		}
	}
	return nil
}

// checkTagKey checks one tag key: 1 to 128 characters of the tag character
// class, not starting with the reserved aws: prefix (sqs.md 5.3).
func checkTagKey(key string) *awserr.Error {
	switch n := utf8.RuneCountInString(key); {
	case n < 1 || n > 128:
		return errTagKeyLength(key)
	case !validTagText(key):
		return errTagCharacters("TagKey", key)
	case strings.HasPrefix(key, "aws:"):
		return errTagKeyReserved(key)
	}
	return nil
}

// validTagText reports whether text holds only what a tag key or value may:
// letters, digits, whitespace and _ . : / = + - @ (sqs.md 5.3).
func validTagText(text string) bool {
	for _, r := range text {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsSpace(r) && !strings.ContainsRune("_.:/=+-@", r) {
			return false
		}
	}
	return true
}

// tagQueue adds tags, overwriting by key (sqs.md 5.3).
func (s *Service) tagQueue(_ context.Context, c *kernel.Call, in *sqsapi.TagQueueInput) (*sqsapi.TagQueueOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	_, err = writeQueue(s, c, q, func(_ *State, queue *Queue, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		tags := maps.Clone(queue.Tags)
		if tags == nil {
			tags = map[string]string{}
		}
		maps.Copy(tags, in.Tags)
		if len(tags) > maxTags {
			return struct{}{}, errTooManyTags(queue.Name)
		}
		queue.Tags = tags
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.TagQueueOutput{}, nil
}

// untagQueue removes tags; unknown keys are ignored (sqs.md 5.3).
func (s *Service) untagQueue(_ context.Context, c *kernel.Call, in *sqsapi.UntagQueueInput) (*sqsapi.UntagQueueOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	_, err = writeQueue(s, c, q, func(_ *State, queue *Queue, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		tags := maps.Clone(queue.Tags)
		for _, k := range in.TagKeys {
			delete(tags, k)
		}
		if len(tags) == 0 {
			tags = nil
		}
		queue.Tags = tags
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.UntagQueueOutput{}, nil
}

// listQueueTags returns the tags, omitting Tags when there are none.
func (s *Service) listQueueTags(_ context.Context, c *kernel.Call, in *sqsapi.ListQueueTagsInput) (*sqsapi.ListQueueTagsOutput, *awserr.Error) {
	q, err := resolveURL(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	tags, err := readQueue(s, q, func(_ *State, queue *Queue) map[string]string { return maps.Clone(queue.Tags) })
	if err != nil {
		return nil, err
	}
	return &sqsapi.ListQueueTagsOutput{Tags: tags}, nil
}

// listDeadLetterSourceQueues lists the queues whose redrive policy names
// the queue, as queueUrls, in pages (sqs.md 5.3).
func (s *Service) listDeadLetterSourceQueues(_ context.Context, c *kernel.Call, in *sqsapi.ListDeadLetterSourceQueuesInput) (*sqsapi.ListDeadLetterSourceQueuesOutput, *awserr.Error) {
	q, err := resolveURL(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	names, err := readQueue(s, q, func(st *State, _ *Queue) []string { return sourcesOf(st, q) })
	if err != nil {
		return nil, err
	}
	page, next, err := paginate(names, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	return &sqsapi.ListDeadLetterSourceQueuesOutput{QueueUrls: urlsOf(c, q.scope, page), NextToken: next}, nil
}

// sourcesOf returns the names of the queues whose redrive policy targets
// the queue dlq.
func sourcesOf(st *State, dlq queueRef) []string {
	var out []string
	for name := range st.Queues {
		if p := st.Queues[name].Attributes.RedrivePolicy; p != nil && p.DeadLetterTarget == dlq {
			out = append(out, name)
		}
	}
	return out
}
