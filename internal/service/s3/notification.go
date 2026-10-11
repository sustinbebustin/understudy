package s3

import (
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindNotification binds the event notification operations. This build
// binds GetBucketNotificationConfiguration, which answers the empty
// configuration of a bucket that has none; PutBucketNotificationConfiguration
// stays unbound and answers 501.
func (s *Service) bindNotification(b s3api.Binder) {
	get := func(in *s3api.GetBucketNotificationConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.GetBucketNotificationConfiguration(reader(s, get, getNotification), registry.Full{}, touches(get))
}

func getNotification(_ *s3api.GetBucketNotificationConfigurationInput, b *Bucket) (*s3api.NotificationConfiguration, *awserr.Error) {
	if b.Notification == nil {
		return s3api.NotConfiguredGetBucketNotificationConfiguration(), nil
	}
	return b.Notification, nil
}

// event is an object change that event notifications report: the event
// name, such as ObjectCreated:Put, and the object it changed.
type event struct {
	name      string
	key       string
	size      int64
	etag      string
	versionID string
}

// notify raises ev for every notification configuration of b that selects
// it, as Deliver effects of the write that commits the change. No
// configuration can be stored in this build, so it raises nothing.
func (*Service) notify(*kernel.Tx, *Bucket, event) {}
