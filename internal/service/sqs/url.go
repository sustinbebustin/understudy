package sqs

import (
	"net/url"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// queueRef locates a queue: its scope and name.
type queueRef struct {
	scope aws.Scope
	name  string
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
// the path is the queue's identity, and Terraform requires exactly those two
// segments (sqs.md finding 6).
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

// resolveURL locates the queue a URL names: the account in its path wins
// over the caller's (cross-account by path) and the region is the call's.
// Anything without the /<account>/<name> path names no queue.
func resolveURL(c *kernel.Call, raw string) (queueRef, *awserr.Error) {
	account, name, ok := parseQueueURL(raw)
	if !ok {
		return queueRef{}, errQueueDoesNotExist()
	}
	scope, err := kernel.ResolveScope(c, kernel.CrossAccount{Account: account})
	if err != nil {
		return queueRef{}, err
	}
	return queueRef{scope: scope, name: name}, nil
}

// existingQueue is resolveURL for a handler that writes to the queue: it
// answers NonExistentQueue before kernel.Write, which creates the scope's
// cell, so a URL naming any account leaves no empty cell behind. The write
// checks again, since the queue can be deleted in between.
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
