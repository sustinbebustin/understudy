package dynamodb

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// startWait bounds how long a call waits for Local to start (ARCHITECTURE.md
// 15: "on first call with a bounded wait"). A cold start takes about 2 s;
// a first pull of several hundred MiB can take minutes, and the client
// should get a retryable answer well before boto3's 60 s read timeout.
const startWait = 30 * time.Second

// ready starts Local when needed and returns where it listens. A Local
// that cannot run here, a *DisabledError, answers 501 with its reason and
// nothing is pulled, as an unbound operation does. A start that outlasts
// startWait goes on in the background and the call answers a retryable
// InternalServerError. Any other failure is DynamoDB's InternalServerError
// with the cause, so the client sees why and other services keep running.
func (s *Service) ready(ctx context.Context) (*url.URL, *awserr.Error) {
	wait, cancel := context.WithTimeout(ctx, startWait)
	defer cancel()
	if err := s.local.Start(wait); err != nil {
		if de, ok := errors.AsType[*DisabledError](err); ok {
			return nil, &awserr.Error{ID: awserr.NotImplemented(), Message: de.Error(), Origin: awserr.Generic{}}
		}
		if ctx.Err() != nil {
			return nil, awserr.RequestAborted()
		}
		if wait.Err() != nil {
			return nil, errStillStarting(startWait)
		}
		return nil, errLocalUnavailable("start DynamoDB Local", err)
	}
	endpoint, ok := s.local.Endpoint()
	if !ok {
		return nil, errLocalUnavailable("reach DynamoDB Local", errStopped)
	}
	return endpoint, nil
}

// errStopped is a Local that stopped between Start and the forward, as at
// shutdown.
var errStopped = errors.New("it stopped while the call was being forwarded")
