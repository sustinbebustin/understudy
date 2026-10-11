package sns

import (
	"context"
	"maps"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindTopics binds the topic control plane.
func (s *Service) bindTopics(b snsapi.Binder) {
	b.CreateTopic(s.createTopic, registry.Full{},
		kernel.WithResources(func(c *kernel.Call, in *snsapi.CreateTopicInput) []aws.ARN {
			return []aws.ARN{topicRef{scope: c.Scope(), name: in.Name}.arn()}
		}))
	b.DeleteTopic(s.deleteTopic, registry.Full{},
		arnResource(func(in *snsapi.DeleteTopicInput) string { return in.TopicArn }))
	b.GetTopicAttributes(s.getTopicAttributes, registry.Full{},
		arnResource(func(in *snsapi.GetTopicAttributesInput) string { return in.TopicArn }))
	b.SetTopicAttributes(s.setTopicAttributes, registry.Partial{Note: "Policy, DeliveryPolicy, KmsMasterKeyId, TracingConfig, ArchivePolicy and the delivery-status attributes are stored and echoed, never enforced"},
		arnResource(func(in *snsapi.SetTopicAttributesInput) string { return in.TopicArn }))
	b.ListTopics(s.listTopics, registry.Full{})
}

// arnResource names the topic or subscription an input's ARN member
// addresses, for fault rules matched by resource; a malformed ARN names
// none.
func arnResource[In any](arn func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(_ *kernel.Call, in *In) []aws.ARN {
		a, err := aws.ParseARN(arn(in))
		if err != nil {
			return nil
		}
		return []aws.ARN{a}
	})
}

// createTopic creates a topic, or returns the existing one's ARN when the
// request's attributes and tags agree with it (sns.md 2.1).
func (s *Service) createTopic(_ context.Context, c *kernel.Call, in *snsapi.CreateTopicInput) (*snsapi.CreateTopicOutput, *awserr.Error) {
	if in.DataProtectionPolicy != nil {
		return nil, errDataProtectionNotImplemented()
	}
	fifo, err := fifoRequested(in.Name, in.Attributes)
	if err != nil {
		return nil, err
	}
	settings, err := createSettings(fifo, in.Attributes)
	if err != nil {
		return nil, err
	}
	tags, err := tagMap(in.Tags)
	if err != nil {
		return nil, err
	}
	if len(tags) > maxTags {
		return nil, errTagLimit()
	}
	ref := topicRef{scope: c.Scope(), name: in.Name}
	_, err = kernel.Write(c, s.cells, ref.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if existing, ok := st.Topics[ref.name]; ok {
			tx.Unchanged()
			return struct{}{}, recreateConflict(ref, &existing, settings, tags)
		}
		t := Topic{Name: ref.name, Kind: Standard{}}
		if fifo {
			t.Kind = FIFO{}
		}
		for _, set := range settings {
			set.apply(&t)
		}
		if len(tags) > 0 {
			t.Tags = tags
		}
		st.Topics[ref.name] = t
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &snsapi.CreateTopicOutput{TopicArn: new(ref.arn().String())}, nil
}

// recreateConflict is the answer to a CreateTopic of an existing topic
// (sns.md 2.1): nil when every requested attribute has the value the topic
// shows and every requested tag is on it, else the recorded conflict.
func recreateConflict(ref topicRef, existing *Topic, settings []topicSetting, tags map[string]string) *awserr.Error {
	current := renderTopicAttributes(ref, existing)
	for _, set := range settings {
		if current[set.name] != set.shown {
			return errReason("Attributes", "Topic already exists with different attributes")
		}
	}
	for k, v := range tags {
		if have, ok := existing.Tags[k]; !ok || have != v {
			return errReason("Tags", "Topic already exists with different tags")
		}
	}
	return nil
}

// fifoRequested checks a CreateTopic name against its FifoTopic attribute:
// a FIFO topic is named <name>.fifo and created with FifoTopic=true, and
// any other combination is "Invalid parameter: Topic Name" (sns.md 2.1).
func fifoRequested(name string, attrs map[string]string) (bool, *awserr.Error) {
	fifo := false
	if v, ok := attrs[attrFifoTopic]; ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false, errReason("Attributes", attrFifoTopic)
		}
		fifo = b
	}
	if !validTopicName(name, fifo) {
		return false, errInvalidParameter("Topic Name")
	}
	return fifo, nil
}

// createSettings checks CreateTopic's attributes other than FifoTopic, in
// name order so the first bad one is reported the same way every time.
func createSettings(fifo bool, attrs map[string]string) ([]topicSetting, *awserr.Error) {
	var out []topicSetting
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		if name == attrFifoTopic {
			continue
		}
		set, detail := topicAttribute(fifo, name, attrs[name])
		if detail != "" {
			return nil, errReason("Attributes", detail)
		}
		out = append(out, set)
	}
	return out, nil
}

// deleteTopic deletes a topic and its subscriptions; deleting a topic that
// does not exist succeeds (sns.md 2.2).
func (s *Service) deleteTopic(_ context.Context, c *kernel.Call, in *snsapi.DeleteTopicInput) (*snsapi.DeleteTopicOutput, *awserr.Error) {
	ref, err := resolveTopic(c, "TopicArn", in.TopicArn)
	if err != nil {
		return nil, err
	}
	_, err = kernel.Write(c, s.cells, ref.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.Topics[ref.name]; !ok {
			tx.Unchanged()
			return struct{}{}, nil
		}
		delete(st.Topics, ref.name)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &snsapi.DeleteTopicOutput{}, nil
}

// getTopicAttributes returns every attribute of a topic as strings.
func (s *Service) getTopicAttributes(_ context.Context, c *kernel.Call, in *snsapi.GetTopicAttributesInput) (*snsapi.GetTopicAttributesOutput, *awserr.Error) {
	ref, err := resolveTopic(c, "TopicArn", in.TopicArn)
	if err != nil {
		return nil, err
	}
	t, ok := s.readTopic(ref)
	if !ok {
		return nil, errTopicNotFound()
	}
	return &snsapi.GetTopicAttributesOutput{Attributes: renderTopicAttributes(ref, &t)}, nil
}

// setTopicAttributes sets one attribute. FifoTopic is fixed at CreateTopic
// (sns.md 2.1); an absent AttributeValue is the empty string.
func (s *Service) setTopicAttributes(_ context.Context, c *kernel.Call, in *snsapi.SetTopicAttributesInput) (*snsapi.SetTopicAttributesOutput, *awserr.Error) {
	ref, err := resolveTopic(c, "TopicArn", in.TopicArn)
	if err != nil {
		return nil, err
	}
	if in.AttributeName == attrFifoTopic {
		return nil, errInvalidParameter("AttributeName")
	}
	value := ""
	if in.AttributeValue != nil {
		value = *in.AttributeValue
	}
	set, detail := topicAttribute(ref.fifo(), in.AttributeName, value)
	if detail != "" {
		return nil, errInvalidParameter(detail)
	}
	_, err = writeTopic(s, c, ref, func(t *Topic, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		set.apply(t)
		if reason := t.largeMessageViolation(); reason != "" {
			return struct{}{}, errLargeMessages(reason)
		}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &snsapi.SetTopicAttributesOutput{}, nil
}

// listTopics lists the caller's topics in its region by name, 100 a page.
func (s *Service) listTopics(_ context.Context, c *kernel.Call, in *snsapi.ListTopicsInput) (*snsapi.ListTopicsOutput, *awserr.Error) {
	scope := c.Scope()
	names, _ := read(s, scope, func(st *State) []string { return slices.Sorted(maps.Keys(st.Topics)) })
	pageNames, next, err := page(names, in.NextToken)
	if err != nil {
		return nil, err
	}
	out := &snsapi.ListTopicsOutput{Topics: make([]snsapi.Topic, 0, len(pageNames)), NextToken: next}
	for _, name := range pageNames {
		out.Topics = append(out.Topics, snsapi.Topic{TopicArn: new(topicRef{scope: scope, name: name}.arn().String())})
	}
	return out, nil
}

// writeTopic runs fn on a copy of the topic ref names, without its expired
// subscriptions, and stores the copy when fn succeeds; a topic that does
// not exist is NotFound.
func writeTopic[R any](s *Service, c *kernel.Call, ref topicRef, fn func(t *Topic, now clock.Instant, tx *kernel.Tx) (R, *awserr.Error)) (R, *awserr.Error) {
	return kernel.Write(c, s.cells, ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (R, *awserr.Error) {
		t, ok := st.writable(ref.name, now)
		if !ok {
			var zero R
			return zero, errTopicNotFound()
		}
		r, err := fn(&t, now, tx)
		if err != nil {
			return r, err
		}
		st.Topics[ref.name] = t
		return r, nil
	})
}
