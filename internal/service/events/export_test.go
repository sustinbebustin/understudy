package events

import "github.com/sustinbebustin/understudy/internal/awserr"

// AdmitNilBus is the bus check on a nil bus reference, which parseBus
// never builds.
func AdmitNilBus() *awserr.Error { return admitDefaultBus(nil) }
