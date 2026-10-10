package outbox

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// send calls the target's operation through the kernel.
func (a *attempt) send(ctx context.Context, t delivery.Target) *awserr.Error {
	switch t := t.(type) {
	case delivery.SQSQueue:
		return a.sendSQS(ctx, a.scope, sqsMessage{queue: t.ARN, body: t.Body, attributes: t.Attributes, groupID: t.GroupID, dedupID: t.DedupID})
	case delivery.LambdaFunction:
		return a.invokeLambda(ctx, t)
	case delivery.SNSTopic:
		return a.publishSNS(ctx, t)
	}
	return awserr.Internalf(&RequestError{Field: "Target", Reason: "is nil"})
}

// operationOf is the operation a delivery to t calls; its IAM actions are
// what the target's resource policy must allow. It is nil for a nil target.
func operationOf(t delivery.Target) *registry.Operation {
	switch t.(type) {
	case delivery.SQSQueue:
		return sqsapi.OpSendMessage.Descriptor()
	case delivery.LambdaFunction:
		return lambdaapi.OpInvoke.Descriptor()
	case delivery.SNSTopic:
		return snsapi.OpPublish.Descriptor()
	}
	return nil
}

// invokeLambda invokes the function with the delivery's event, with the
// invocation type the target names (Event for SNS, S3 and EventBridge).
func (a *attempt) invokeLambda(ctx context.Context, f delivery.LambdaFunction) *awserr.Error {
	invocation := lambdaapi.InvocationType(f.Invocation.String())
	in := &lambdaapi.InvokeInput{FunctionName: f.ARN.String(), InvocationType: &invocation, Payload: f.Event}
	if f.Qualifier != "" {
		in.Qualifier = &f.Qualifier
	}
	_, err := kernel.Invoke(ctx, a.e.k, a.c, lambdaapi.OpInvoke, a.as(a.scope), in)
	return err
}

// publishSNS publishes the delivery's message to the topic.
func (a *attempt) publishSNS(ctx context.Context, t delivery.SNSTopic) *awserr.Error {
	topic := t.ARN.String()
	in := &snsapi.PublishInput{TopicArn: &topic, Message: t.Message}
	if t.Subject != "" {
		in.Subject = &t.Subject
	}
	if len(t.Attributes) > 0 {
		in.MessageAttributes = make(map[string]snsapi.MessageAttributeValue, len(t.Attributes))
		for _, attr := range t.Attributes {
			in.MessageAttributes[attr.Name] = snsAttribute(attr.Value)
		}
	}
	_, err := kernel.Invoke(ctx, a.e.k, a.c, snsapi.OpPublish, a.as(a.scope), in)
	return err
}

// snsAttribute is a message attribute as SNS takes it.
func snsAttribute(v delivery.AttributeValue) snsapi.MessageAttributeValue {
	out := snsapi.MessageAttributeValue{DataType: v.DataType()}
	switch v := v.(type) {
	case delivery.StringAttribute:
		out.StringValue = &v.Value
	case delivery.NumberAttribute:
		out.StringValue = &v.Value
	case delivery.BinaryAttribute:
		out.BinaryValue = v.Value
	}
	return out
}

// sqsMessage is a SendMessage of a delivery or of its dead letter.
type sqsMessage struct {
	queue      aws.ARN
	body       string
	attributes []delivery.MessageAttribute
	// groupID and dedupID are set for FIFO queues only.
	groupID, dedupID string
}

// sendSQS sends m to its queue in scope.
func (a *attempt) sendSQS(ctx context.Context, scope aws.Scope, m sqsMessage) *awserr.Error {
	in := &sqsapi.SendMessageInput{QueueUrl: queueURL(m.queue), MessageBody: m.body}
	if len(m.attributes) > 0 {
		in.MessageAttributes = make(map[string]sqsapi.MessageAttributeValue, len(m.attributes))
		for _, attr := range m.attributes {
			in.MessageAttributes[attr.Name] = sqsAttribute(attr.Value)
		}
	}
	if m.groupID != "" {
		in.MessageGroupId = &m.groupID
	}
	if m.dedupID != "" {
		in.MessageDeduplicationId = &m.dedupID
	}
	_, err := kernel.Invoke(ctx, a.e.k, a.c, sqsapi.OpSendMessage, a.as(scope), in)
	return err
}

// queueURL is the AWS form of the queue's URL. SQS reads the account and
// name from the path and ignores the host.
func queueURL(queue aws.ARN) string {
	return "https://sqs." + queue.Region + ".amazonaws.com/" + queue.Account + "/" + queue.Resource
}

// sqsAttribute is an SNS or S3 message attribute as SQS takes it.
func sqsAttribute(v delivery.AttributeValue) sqsapi.MessageAttributeValue {
	out := sqsapi.MessageAttributeValue{DataType: v.DataType()}
	switch v := v.(type) {
	case delivery.StringAttribute:
		out.StringValue = &v.Value
	case delivery.NumberAttribute:
		out.StringValue = &v.Value
	case delivery.BinaryAttribute:
		out.BinaryValue = v.Value
	}
	return out
}
