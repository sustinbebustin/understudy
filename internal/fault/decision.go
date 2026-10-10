package fault

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Hook is the point in the emulator that calls Store.Evaluate. Each hook
// runs the rules of one phase whose action belongs there. The zero Hook runs
// no rule.
type Hook uint8

// Hook values.
const (
	// BeforeHandler is pipeline step 8, after validation and before the
	// handler, and the DynamoDB proxy before forwarding. It runs Request-phase
	// Error, Throttle and Latency rules.
	BeforeHandler Hook = iota + 1
	// AfterHandler is pipeline step 10. The kernel calls it only when the
	// handler succeeded, so a hit always replaces a committed result. It runs
	// Request-phase AfterCommit and StatusOKError rules.
	AfterHandler
	// DeliveryAttempt is the delivery engine, once per attempt. It runs
	// Delivery-phase Latency and DropDelivery rules.
	DeliveryAttempt
)

// runs reports whether rule r belongs to hook h.
func (h Hook) runs(r Rule) bool {
	switch r.Action.(type) {
	case Error, Throttle:
		return h == BeforeHandler
	case AfterCommit, StatusOKError:
		return h == AfterHandler
	case DropDelivery:
		return h == DeliveryAttempt
	case Latency:
		return (h == BeforeHandler && r.Phase == Request) || (h == DeliveryAttempt && r.Phase == Delivery)
	default:
		return false
	}
}

// MatchInput describes the call or delivery attempt being evaluated.
//
// At BeforeHandler and AfterHandler it describes the call: the operation's
// service, the ARNs the call touches and the scope it runs in. At
// DeliveryAttempt it describes the target of the attempt: for an SNSToSQS
// attempt Service is sqs, Touched holds the queue ARN and Scope is the
// queue's account and region, so a rule's Services, Resource, Accounts and
// Regions select deliveries by where they land.
type MatchInput struct {
	Service ref.ServiceID
	// Op is the operation at BeforeHandler and AfterHandler; zero at
	// DeliveryAttempt.
	Op ref.OperationID
	// Mechanism is the delivery path at DeliveryAttempt; zero otherwise.
	Mechanism delivery.Mechanism
	Touched   []aws.ARN
	Scope     aws.Scope
	// Origin is where the call came from; at DeliveryAttempt it is always
	// ref.Internal{Via: Mechanism.Via()}.
	Origin ref.Origin
}

// Decision is what the caller of Evaluate does with the call. BeforeHandler
// decides None, Fail or Delay; AfterHandler None, FailAfterCommit or
// FailStatusOK; DeliveryAttempt None, Delay or Drop. Its variants
// are named apart from the Action variants that produce them: a Latency
// action decides Delay, AfterCommit decides FailAfterCommit and
// StatusOKError decides FailStatusOK.
//
//sumtype:decl
type Decision interface{ isDecision() }

// Injection identifies one hit of a rule, for the FaultInjected journal
// event: Hit is this hit's 1-based number and Of is the NextN budget, or 0
// for a rule without one.
type Injection struct {
	Rule ref.RuleID
	Hit  int
	Of   int
}

// None lets the call proceed untouched.
type None struct{}

// Fail answers the call with Err before the handler runs.
type Fail struct {
	Err *awserr.Error
	Injection
}

// Delay holds the call for Wall of wall time, then applies Then, the
// decision of the rules after this one; Then may itself be a Delay.
//
// Evaluate decides Then before it returns, so every Injection inside Then is
// already a hit: its rule's count has advanced and its NextN budget is
// spent. The caller journals each of them, including when it abandons the
// wait because the client left and Then is never applied.
type Delay struct {
	Wall time.Duration
	Then Decision
	Injection
}

// FailAfterCommit answers with Err instead of the result of the handler that
// already ran and committed.
type FailAfterCommit struct {
	Err *awserr.Error
	Injection
}

// FailStatusOK answers 200 with Err in the body instead of the result of the
// handler that already ran.
type FailStatusOK struct {
	Err *awserr.Error
	Injection
}

// Drop discards the delivery attempt.
type Drop struct{ Injection }

func (None) isDecision()            {}
func (Fail) isDecision()            {}
func (Delay) isDecision()           {}
func (FailAfterCommit) isDecision() {}
func (FailStatusOK) isDecision()    {}
func (Drop) isDecision()            {}
