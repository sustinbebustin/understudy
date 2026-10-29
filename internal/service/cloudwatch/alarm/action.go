package alarm

import "github.com/sustinbebustin/understudy/internal/aws"

// Action is one ARN of an alarm's OKActions, AlarmActions or
// InsufficientDataActions, by what understudy can do with it
// (cloudwatch-alarms.md 7.2).
//
//sumtype:decl
type Action interface{ isAction() }

// SNSAction publishes the notification to a topic
// (arn:aws:sns:region:account-id:name).
type SNSAction struct {
	Topic aws.ARN
}

// LambdaAction invokes a function asynchronously
// (arn:aws:lambda:region:account-id:function:name[:version or alias]);
// Function is the unqualified ARN, Qualifier the version or alias or "".
type LambdaAction struct {
	Function  aws.ARN
	Qualifier string
}

// NotRunAction is an action understudy cannot run, journaled as not run
// (cloudwatch-alarms.md 12.7).
type NotRunAction struct {
	ARN  string
	Kind NotRunKind
}

func (SNSAction) isAction()    {}
func (LambdaAction) isAction() {}
func (NotRunAction) isAction() {}

// NotRunKind is why an action is not run.
type NotRunKind uint8

// NotRunKind values.
const (
	// NotRunEC2: arn:aws:automate:region:ec2:stop|terminate|reboot|recover
	// or arn:aws:swf:region:account-id:action/actions/AWS_EC2.InstanceId.*.
	NotRunEC2 NotRunKind = iota + 1
	// NotRunAutoScaling: arn:aws:autoscaling:...:scalingPolicy:...
	NotRunAutoScaling
	// NotRunSystemsManager: arn:aws:ssm:...:opsitem:... or
	// arn:aws:ssm-incidents::...:responseplan/...
	NotRunSystemsManager
	// NotRunInvestigation: arn:aws:aiops:...:investigation-group:...
	NotRunInvestigation
	// NotRunUnrecognized: an ARN that names none of the documented kinds.
	NotRunUnrecognized
)

// String names the kind: "ec2", "autoscaling", "ssm", "investigation" or
// "unrecognized".
func (NotRunKind) String() string {
	// not implemented yet
	return ""
}

// ParseAction classifies an action ARN by the forms of the model doc of
// PutMetricAlarmInput$AlarmActions (cloudwatch-alarms.md 6.1, 7.2).
func ParseAction(arn string) Action {
	// not implemented yet
	return NotRunAction{ARN: arn, Kind: NotRunUnrecognized}
}
