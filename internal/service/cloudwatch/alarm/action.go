package alarm

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
)

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
func (k NotRunKind) String() string {
	switch k {
	case NotRunEC2:
		return "ec2"
	case NotRunAutoScaling:
		return "autoscaling"
	case NotRunSystemsManager:
		return "ssm"
	case NotRunInvestigation:
		return "investigation"
	case NotRunUnrecognized:
		return "unrecognized"
	default:
		return "unrecognized"
	}
}

// notRunForm is the resource prefix of the documented action kind
// understudy does not run for an ARN service (cloudwatch-alarms.md 7.2);
// ok is false for a service with no such form.
func notRunForm(service string) (prefix string, kind NotRunKind, ok bool) {
	switch service {
	case "swf":
		return "action/actions/AWS_EC2.InstanceId.", NotRunEC2, true
	case "autoscaling":
		return "scalingPolicy:", NotRunAutoScaling, true
	case "ssm":
		return "opsitem:", NotRunSystemsManager, true
	case "ssm-incidents":
		return "responseplan/", NotRunSystemsManager, true
	case "aiops":
		return "investigation-group:", NotRunInvestigation, true
	}
	return "", 0, false
}

// ParseAction classifies an action ARN by the forms of the model doc of
// PutMetricAlarmInput$AlarmActions (cloudwatch-alarms.md 6.1, 7.2).
func ParseAction(arn string) Action {
	a, err := aws.ParseARN(arn)
	if err != nil {
		return NotRunAction{ARN: arn, Kind: NotRunUnrecognized}
	}
	notRun := func(k NotRunKind) Action { return NotRunAction{ARN: arn, Kind: k} }
	if a.Service == "sns" || a.Service == "lambda" {
		// A topic or function outside every account and Region understudy
		// serves can never be delivered to; it is journaled as not run
		// rather than failing the transition's write.
		if _, err := aws.ScopeOf(a); err != nil {
			return notRun(NotRunUnrecognized)
		}
	}
	switch a.Service {
	case "sns":
		if !strings.Contains(a.Resource, ":") {
			return SNSAction{Topic: a}
		}
	case "lambda":
		rest, ok := strings.CutPrefix(a.Resource, "function:")
		name, qualifier, _ := strings.Cut(rest, ":")
		if ok && name != "" && !strings.Contains(qualifier, ":") {
			a.Resource = "function:" + name
			return LambdaAction{Function: a, Qualifier: qualifier}
		}
	case "automate":
		switch a.Resource {
		case "stop", "terminate", "reboot", "recover":
			if a.Account == "ec2" {
				return notRun(NotRunEC2)
			}
		}
	default:
		if prefix, kind, ok := notRunForm(a.Service); ok && strings.HasPrefix(a.Resource, prefix) {
			return notRun(kind)
		}
	}
	return notRun(NotRunUnrecognized)
}
