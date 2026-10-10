package fault_test

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// The test catalogue stands in for the kernel's registry view. Its
// identities follow the SQS, Lambda and S3 rows of error-matrix.md, sqs.md
// 2.4 and the vendored Smithy models; test files may build Identity literals.

var (
	sendMessage      = ref.OperationID{Service: "sqs", Name: "SendMessage"}
	receiveMessage   = ref.OperationID{Service: "sqs", Name: "ReceiveMessage"}
	changeVisibility = ref.OperationID{Service: "sqs", Name: "ChangeMessageVisibility"}
	invoke           = ref.OperationID{Service: "lambda", Name: "Invoke"}
	completeUpload   = ref.OperationID{Service: "s3", Name: "CompleteMultipartUpload"}
	publish          = ref.OperationID{Service: "sns", Name: "Publish"}
	getItem          = ref.OperationID{Service: "dynamodb", Name: "GetItem"}
	putItem          = ref.OperationID{Service: "dynamodb", Name: "PutItem"}
	decrypt          = ref.OperationID{Service: "kms", Name: "Decrypt"}
)

// dynamoDBThrottling is DynamoDB's modeled ThrottlingException, which shares
// its name with the generic catalogue's.
func dynamoDBThrottling() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.dynamodb#ThrottlingException", JSONType: "com.amazonaws.dynamodb#ThrottlingException",
		QueryCode: "ThrottlingException", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
	}
}

func queueDoesNotExist() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.sqs#QueueDoesNotExist", JSONType: "com.amazonaws.sqs#QueueDoesNotExist",
		QueryCode: "AWS.SimpleQueueService.NonExistentQueue", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
	}
}

func invalidMessageContents() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.sqs#InvalidMessageContents", JSONType: "com.amazonaws.sqs#InvalidMessageContents",
		QueryCode: "InvalidMessageContents", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
	}
}

// invalidParameterValue is an overlay-added SQS shape: the model does not
// declare it, AWS sends it (sqs.md 2.4).
func invalidParameterValue() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.sqs#InvalidParameterValue", JSONType: "InvalidParameterValueException",
		QueryCode: "InvalidParameterValue", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
	}
}

// messageNotInflight is an SQS shape with no message member.
func messageNotInflight() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.sqs#MessageNotInflight", JSONType: "com.amazonaws.sqs#MessageNotInflight",
		QueryCode: "AWS.SimpleQueueService.MessageNotInflight", Status: 400, Fault: awserr.Sender, MessageKey: awserr.NoMessage,
	}
}

func requestThrottled() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.sqs#RequestThrottled", JSONType: "com.amazonaws.sqs#RequestThrottled",
		QueryCode: "RequestThrottled", Status: 403, Fault: awserr.Sender, MessageKey: awserr.MessageLower, Retryable: true,
	}
}

// replicatedWriteConflict is a 400 DynamoDB error outside the SDK throttle
// set that the model marks retryable.
func replicatedWriteConflict() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.dynamodb#ReplicatedWriteConflictException", JSONType: "com.amazonaws.dynamodb#ReplicatedWriteConflictException",
		QueryCode: "ReplicatedWriteConflictException", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower, Retryable: true,
	}
}

// lambdaKMSInvalidState and kmsInvalidState share a name, but Lambda sends
// it as a 502 the SDKs retry and KMS as a 409 they do not.
func lambdaKMSInvalidState() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.lambda#KMSInvalidStateException", JSONType: "KMSInvalidStateException",
		QueryCode: "KMSInvalidStateException", Status: 502, Fault: awserr.Receiver, MessageKey: awserr.MessageUpper,
	}
}

func kmsInvalidState() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.kms#KMSInvalidStateException", JSONType: "KMSInvalidStateException",
		QueryCode: "KMSInvalidStateException", Status: 409, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
	}
}

func tooManyRequests() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.lambda#TooManyRequestsException", JSONType: "TooManyRequestsException",
		QueryCode: "TooManyRequestsException", Status: 429, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
	}
}

func s3InternalError() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.s3#InternalError", QueryCode: "InternalError", Status: 500,
		Fault: awserr.Receiver, MessageKey: awserr.MessageUpper,
	}
}

func slowDown() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.s3#SlowDown", QueryCode: "SlowDown", Status: 503,
		Fault: awserr.Receiver, MessageKey: awserr.MessageUpper,
	}
}

// s3RequestTimeout is S3's 400 for a body that stopped arriving; the model
// does not declare it, AWS sends it (S3 ErrorResponses), and the SDKs retry
// it by code.
func s3RequestTimeout() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.s3#RequestTimeout", QueryCode: "RequestTimeout", Status: 400,
		Fault: awserr.Sender, MessageKey: awserr.MessageUpper,
	}
}

// s3AccessDenied is a 4xx S3 error, one the SDKs do not retry when it comes
// with its own status.
func s3AccessDenied() awserr.Identity {
	return awserr.Identity{
		Shape: "com.amazonaws.s3#AccessDenied", QueryCode: "AccessDenied", Status: 403,
		Fault: awserr.Sender, MessageKey: awserr.MessageUpper,
	}
}

type testCatalogue struct{}

func (testCatalogue) Errors(op ref.OperationID) ([]awserr.Identity, bool) {
	generic := awserr.Catalogue(awserr.JSONFamily)
	switch op {
	case sendMessage:
		return append([]awserr.Identity{queueDoesNotExist(), invalidMessageContents(), invalidParameterValue(), requestThrottled()}, generic...), true
	case receiveMessage:
		return append([]awserr.Identity{queueDoesNotExist(), requestThrottled()}, generic...), true
	case changeVisibility:
		return append([]awserr.Identity{queueDoesNotExist(), messageNotInflight(), requestThrottled()}, generic...), true
	case invoke:
		return append([]awserr.Identity{tooManyRequests(), lambdaKMSInvalidState()}, generic...), true
	case completeUpload:
		return []awserr.Identity{s3InternalError(), slowDown(), s3AccessDenied(), s3RequestTimeout()}, true
	case publish:
		return awserr.Catalogue(awserr.QueryFamily), true
	case getItem:
		return append([]awserr.Identity{dynamoDBThrottling()}, generic...), true
	case putItem:
		return append([]awserr.Identity{dynamoDBThrottling(), replicatedWriteConflict()}, generic...), true
	case decrypt:
		return append([]awserr.Identity{kmsInvalidState()}, generic...), true
	default:
		return nil, false
	}
}

func (testCatalogue) Throttle(svc ref.ServiceID) awserr.Identity {
	switch svc {
	case "sqs":
		return requestThrottled()
	case "lambda":
		return tooManyRequests()
	case "s3":
		return slowDown()
	case "sns":
		return awserr.ThrottlingException()
	default:
		return awserr.Identity{}
	}
}

func newStore(seed uint64) *fault.Store {
	return fault.New(&seed, ids.NewSeeded(seed), testCatalogue{})
}

func mustAdd(t interface {
	Helper()
	Fatalf(string, ...any)
}, s *fault.Store, r fault.Rule,
) ref.RuleID {
	t.Helper()
	id, err := s.Add(r)
	if err != nil {
		t.Fatalf("Add(%+v): %v", r, err)
	}
	return id
}

func defaultScope() aws.Scope {
	return aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}
}

func queueARN(name string) aws.ARN {
	return aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: name}
}

// call is an external call of op that touches the orders queue.
func call(op ref.OperationID) fault.MatchInput {
	return fault.MatchInput{
		Service: op.Service,
		Op:      op,
		Touched: []aws.ARN{queueARN("orders")},
		Scope:   defaultScope(),
		Origin:  ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain},
	}
}
