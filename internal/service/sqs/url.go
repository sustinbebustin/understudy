package sqs

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// queueRef locates a queue: its scope and name. The state stores a
// reference to another queue as one, written as the queue's ARN.
type queueRef struct {
	scope aws.Scope
	name  string
}

// MarshalText writes q as its ARN.
func (q queueRef) MarshalText() ([]byte, error) { return []byte(q.arn().String()), nil }

// UnmarshalText reads a queue reference MarshalText wrote; anything else
// fails the load, rather than leaving a reference that names no queue.
func (q *queueRef) UnmarshalText(text []byte) error {
	r, ok := parseQueueARN(string(text))
	if !ok {
		return fmt.Errorf("%q is not an SQS queue ARN of the form arn:aws:sqs:<region>:<account>:<name>", text)
	}
	*q = r
	return nil
}

// arn returns arn:aws:sqs:<region>:<account>:<name>.
func (q queueRef) arn() aws.ARN {
	return aws.ARN{
		Partition: q.scope.Partition.String(),
		Service:   "sqs",
		Region:    q.scope.Region.String(),
		Account:   q.scope.Account.String(),
		Resource:  q.name,
	}
}

// waitKey is what receivers of the queue wait on.
func (q queueRef) waitKey() store.WaitKey { return store.WaitKey("sqs:" + q.arn().String()) }

// queueURL is <scheme>://<host as received>/<account>/<name> (ADR 0013):
// the path is the queue's identity, and Terraform requires exactly those
// two segments (sqs.md 1.6).
func queueURL(c *kernel.Call, q queueRef) string {
	return formatQueueURL(c.Endpoint(), q.scope.Account, q.name)
}

func formatQueueURL(e kernel.Endpoint, account aws.AccountID, name string) string {
	return e.String() + "/" + account.String() + "/" + name
}

// parseQueueURL reads the account and queue name from a queue URL's path,
// /<account>/<name>, ignoring the host, since clients post to their
// configured endpoint. A trailing slash is accepted, as the documentation
// examples show one.
func parseQueueURL(raw string) (aws.AccountID, string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return aws.AccountID{}, "", false
	}
	account, name, ok := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/"), "/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return aws.AccountID{}, "", false
	}
	id, err := aws.ParseAccountID(account)
	if err != nil {
		return aws.AccountID{}, "", false
	}
	return id, name, true
}

// isQueueName reports whether s is a queue name CreateQueue could have
// taken: 1 to 80 characters of letters, digits, hyphens and underscores,
// the last five of a FIFO queue's name ".fifo".
func isQueueName(s string) bool {
	return len(s) <= 80 && isSQSToken(strings.TrimSuffix(s, ".fifo"))
}

// resolveURL locates the queue a URL names: the account in its path wins
// over the caller's (cross-account by path) and the region is the call's,
// whatever region the URL's host names (sqs.md 13.6). A bare
// queue name names that queue in the caller's account, as AWS accepts one
// in QueueUrl (sqs.md 3). Anything else is InvalidAddress, as AWS answers
// an address that is no queue's; a well-formed URL or name whose queue is
// missing is the caller's NonExistentQueue to answer.
func resolveURL(c *kernel.Call, raw string) (queueRef, *awserr.Error) {
	account, name, ok := parseQueueURL(raw)
	switch {
	case ok:
	case isQueueName(raw):
		account, name = c.Scope().Account, raw
	default:
		return queueRef{}, errInvalidAddress(raw)
	}
	scope, err := kernel.ResolveScope(c, kernel.CrossAccount{Account: account})
	if err != nil {
		return queueRef{}, err
	}
	return queueRef{scope: scope, name: name}, nil
}

// parseQueueARN reads an SQS queue ARN into its scope and name; ok is
// false for anything else, and for an ARN that queueRef.arn would not
// write back as it is.
func parseQueueARN(raw string) (queueRef, bool) {
	a, err := aws.ParseARN(raw)
	if err != nil || a.Service != "sqs" || strings.Contains(a.Resource, ":") {
		return queueRef{}, false
	}
	q, ok := queueOf(a)
	if !ok || q.arn() != a {
		return queueRef{}, false
	}
	return q, true
}

// queueOf reads a queue ARN back into its scope and name.
func queueOf(arn aws.ARN) (queueRef, bool) {
	scope, err := aws.ScopeOf(arn)
	if err != nil {
		return queueRef{}, false
	}
	return queueRef{scope: scope, name: arn.Resource}, true
}

// existingQueue is resolveURL for a handler: it answers NonExistentQueue
// before kernel.Write, which creates the scope's cell, so a URL naming any
// account leaves no empty cell behind. A write checks again, since the
// queue can be deleted in between.
func (s *Service) existingQueue(c *kernel.Call, raw string) (queueRef, *awserr.Error) {
	q, err := resolveURL(c, raw)
	if err != nil {
		return queueRef{}, err
	}
	if !s.exists(q) {
		return queueRef{}, errQueueDoesNotExist()
	}
	return q, nil
}

// exists reports whether q exists, without creating a cell for its scope.
func (s *Service) exists(q queueRef) bool {
	_, err := readQueue(s, q, func(*State, *Queue) struct{} { return struct{}{} })
	return err == nil
}

// readQueue runs fn on q under its cell's lock, without creating a cell.
// fn must return values that do not alias the state.
func readQueue[R any](s *Service, q queueRef, fn func(st *State, queue *Queue) R) (R, *awserr.Error) {
	var zero R
	cell, ok := s.cells.Lookup(q.scope)
	if !ok {
		return zero, errQueueDoesNotExist()
	}
	type result struct {
		r  R
		ok bool
	}
	got := store.Read(cell, func(st *State) result {
		queue, ok := st.Queues[q.name]
		if !ok {
			return result{}
		}
		return result{r: fn(st, &queue), ok: true}
	})
	if !got.ok {
		return zero, errQueueDoesNotExist()
	}
	return got.r, nil
}

// writeQueue runs fn on a working copy of q (Queue.clone) through
// kernel.Write and stores the copy in q's place when fn succeeds, so the
// stored queue is replaced, never changed in place, and a failed fn leaves
// it as it was. It answers NonExistentQueue when the queue is gone. fn
// changes any other queue the same way, through a clone it stores back.
func writeQueue[R any](s *Service, c *kernel.Call, q queueRef, fn func(st *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (R, *awserr.Error)) (R, *awserr.Error) {
	return kernel.Write(c, s.cells, q.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (R, *awserr.Error) {
		stored, ok := st.Queues[q.name]
		if !ok {
			var zero R
			return zero, errQueueDoesNotExist()
		}
		queue := stored.clone()
		r, err := fn(st, &queue, now, tx)
		if err == nil {
			st.Queues[q.name] = queue
		}
		return r, err
	})
}

// queueByURL names the queue an input addresses by URL as the call's
// resource, before fault rules run; a URL that names no queue names
// nothing, and the handler answers it.
func queueByURL[In any](queueURLOf func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		q, err := resolveURL(c, queueURLOf(in))
		if err != nil {
			return nil
		}
		return []aws.ARN{q.arn()}
	})
}
