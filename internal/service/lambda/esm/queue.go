package esm

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/store"
)

// QueueURL is the URL the poller addresses queue by. SQS reads only the
// path, /<account>/<name>; the host is AWS's regional endpoint, which no
// request ever reaches, since the calls stay inside the kernel.
func QueueURL(queue aws.ARN) string {
	return "https://sqs." + queue.Region + ".amazonaws.com/" + queue.Account + "/" + queue.Resource
}

// QueueWaitKey is what SQS wakes when a message of queue may have become
// receivable: a send, or a delay or visibility timeout ending. It mirrors
// the key the sqs package's receivers wait on, which that package does not
// export; the poller tests catch a drift.
func QueueWaitKey(queue aws.ARN) store.WaitKey {
	return store.WaitKey("sqs:" + queue.String())
}
