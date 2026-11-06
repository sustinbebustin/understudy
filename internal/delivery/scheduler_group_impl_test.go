package delivery_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/delivery"
)

// GroupARN is the schedule group's ARN, the aws:SourceArn Scheduler
// assumes the execution role with (scheduler.md 2.3, 9); zero for an ARN
// that names no schedule.
func TestSchedulerInvocationGroupARN(t *testing.T) {
	for _, tc := range []struct{ schedule, want string }{
		{"arn:aws:scheduler:us-east-1:000000000000:schedule/default/s", "arn:aws:scheduler:us-east-1:000000000000:schedule-group/default"},
		{"arn:aws:scheduler:eu-west-1:111122223333:schedule/g/nightly", "arn:aws:scheduler:eu-west-1:111122223333:schedule-group/g"},
		{"arn:aws:scheduler:us-east-1:000000000000:schedule-group/g", ""},
		{"arn:aws:scheduler:us-east-1:000000000000:schedule/g", ""},
	} {
		a, err := aws.ParseARN(tc.schedule)
		if err != nil {
			t.Fatal(err)
		}
		got := delivery.SchedulerInvocation{ScheduleARN: a}.GroupARN()
		if (tc.want == "" && got != aws.ARN{}) || (tc.want != "" && got.String() != tc.want) {
			t.Errorf("GroupARN of %s = %q, want %q", tc.schedule, got.String(), tc.want)
		}
	}
}
