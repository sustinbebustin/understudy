package events

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// AdmitNilBus is the bus check on a nil bus reference, which parseBus
// never builds.
func AdmitNilBus() *awserr.Error { return (&Service{}).admitDefaultBus(aws.Scope{}, nil) }
