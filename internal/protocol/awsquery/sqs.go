package awsquery

import (
	"net/http"
	"strings"

	"github.com/sustinbebustin/understudy/internal/registry"
)

// sqsService is SQS, whose legacy Query clients also post to the queue URL
// (sqs.md 2.3).
const sqsService = "sqs"

// queuePath returns the path of r when it is an SQS queue URL path,
// /<12-digit account>/<queue name>, and the request is for SQS.
func queuePath(r *http.Request, svc *registry.Service) (string, bool) {
	if svc.ID != sqsService {
		return "", false
	}
	account, queue, ok := strings.Cut(strings.Trim(r.URL.Path, "/"), "/")
	if !ok || queue == "" || strings.Contains(queue, "/") || !isAccount(account) {
		return "", false
	}
	return "/" + account + "/" + queue, true
}

// servedOnQueuePath reports whether an SQS action is served on a queue
// path. CreateQueue and ListQueues are account-scoped, and SQS answers them
// there with InvalidAction (sqs.md 2.3); every other action, GetQueueUrl
// included, is accepted.
func servedOnQueuePath(op *registry.Operation) bool {
	return op.ID.Name != "CreateQueue" && op.ID.Name != "ListQueues"
}

// queueURL is the queue URL of a request on a queue path: the scheme and
// Host the client addressed, then the path. A QueueUrl parameter in the
// form takes precedence over it (sqs.md 2.3).
func queueURL(r *http.Request, path string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}

func isAccount(s string) bool {
	if len(s) != 12 {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
