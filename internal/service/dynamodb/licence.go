package dynamodb

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// startWait bounds how long a call waits for Local to start (ARCHITECTURE.md
// 15: "on first call with a bounded wait"). A cold start takes about 2 s;
// a first pull of several hundred MiB can take minutes, and the client
// should get a retryable answer well before boto3's 60 s read timeout.
const startWait = 30 * time.Second

// ready starts Local when needed and returns where it listens. A licence
// that does not cover the pinned image answers 501 with the notice, which
// names the flag and the environment variable that accept it (ADR 0014);
// nothing is pulled. A start that outlasts startWait goes on in the
// background and the call answers a retryable InternalServerError. Any
// other failure is DynamoDB's InternalServerError with the cause, so the
// client sees why and other services keep running.
func (s *Service) ready(ctx context.Context) (*url.URL, *awserr.Error) {
	wait, cancel := context.WithTimeout(ctx, startWait)
	defer cancel()
	if err := s.local.Start(wait); err != nil {
		if le, ok := errors.AsType[*ddblocal.LicenceError](err); ok {
			return nil, &awserr.Error{ID: awserr.NotImplemented(), Message: le.Error(), Origin: awserr.Generic{}}
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
