package scheduler

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// defaultGroup is the group a schedule belongs to when GroupName is not
// sent (scheduler.md 3.1, 4.2).
const defaultGroup = "default"

// groupName is the group a request names, default when it names none.
func groupName(sent *string) string {
	if sent == nil || *sent == "" {
		return defaultGroup
	}
	return *sent
}

// scheduleRef locates a schedule: its scope, group and name.
type scheduleRef struct {
	scope aws.Scope
	group string
	name  string
}

// arn is arn:<partition>:scheduler:<region>:<account>:schedule/<group>/<name>
// (scheduler.md 2.3).
func (r scheduleRef) arn() aws.ARN {
	return aws.ARN{
		Partition: r.scope.Partition.String(),
		Service:   "scheduler",
		Region:    r.scope.Region.String(),
		Account:   r.scope.Account.String(),
		Resource:  "schedule/" + r.group + "/" + r.name,
	}
}

// parseScheduleARN reads a schedule ARN, the key of its fire timer; false
// for anything else.
func parseScheduleARN(s string) (scheduleRef, bool) {
	a, err := aws.ParseARN(s)
	if err != nil || a.Service != "scheduler" {
		return scheduleRef{}, false
	}
	rest, ok := strings.CutPrefix(a.Resource, "schedule/")
	group, name, both := strings.Cut(rest, "/")
	if !ok || !both || group == "" || name == "" || strings.Contains(name, "/") {
		return scheduleRef{}, false
	}
	scope, err := aws.ScopeOf(a)
	if err != nil {
		return scheduleRef{}, false
	}
	return scheduleRef{scope: scope, group: group, name: name}, true
}
