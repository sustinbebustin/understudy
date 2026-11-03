package server_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/server"
)

// A schedule with a Step Functions target starts one execution of the
// served state machine at its scheduled instant, with the payload as the
// execution's input (scheduler.md 6.4) and understudy's 16-hex execution
// name (UNVERIFIED SCH-07: the name AWS gives is unrecorded).
func TestSchedulerStartsAStateMachineExecution(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceOff)
	sfnRoleTrusting(t, endpoint, "states.amazonaws.com")
	schRoleTrusting(t, endpoint, schTrust)
	w := createMachine(t, dummy, endpoint, "flow", `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`, "")
	if w.status != http.StatusOK {
		t.Fatalf("CreateStateMachine = %d %s", w.status, w.body)
	}
	var created struct{ StateMachineArn string }
	if err := json.Unmarshal([]byte(w.body), &created); err != nil {
		t.Fatal(err)
	}
	start := strconv.FormatInt(frozenAt.Add(30*time.Minute).Unix(), 10)
	input, err := json.Marshal(`{"attempt":"<aws.scheduler.attempt-number>"}`)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"ScheduleExpression":"rate(1 hour)","StartDate":` + start + `,"FlexibleTimeWindow":{"Mode":"OFF"},` +
		`"Target":{"Arn":"` + created.StateMachineArn + `","RoleArn":"` + schRole + `","Input":` + string(input) + `}}`
	if w := schCall(t, dummy, endpoint, http.MethodPost, "/schedules/s", body); w.status != http.StatusOK {
		t.Fatalf("CreateSchedule = %d %s", w.status, w.body)
	}
	schAdvance(t, c)

	w = sfnCall(t, dummy, endpoint, "ListExecutions", `{"stateMachineArn":"`+created.StateMachineArn+`"}`)
	var listed struct {
		Executions []struct{ ExecutionArn, Name string }
	}
	if err := json.Unmarshal([]byte(w.body), &listed); err != nil || w.status != http.StatusOK {
		t.Fatalf("ListExecutions = %d %s: %v", w.status, w.body, err)
	}
	// The name is the invocation's execution id, a UUID (SCH-07, recorded
	// 2026-11-03; the name AWS gives is unrecorded, scheduler.md 6.4).
	if len(listed.Executions) != 1 || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(listed.Executions[0].Name) {
		t.Fatalf("executions = %+v, want one named with a UUID", listed.Executions)
	}
	w = sfnCall(t, dummy, endpoint, "DescribeExecution", `{"executionArn":"`+listed.Executions[0].ExecutionArn+`"}`)
	var described struct{ Input string }
	if err := json.Unmarshal([]byte(w.body), &described); err != nil || w.status != http.StatusOK {
		t.Fatalf("DescribeExecution = %d %s: %v", w.status, w.body, err)
	}
	if described.Input != `{"attempt":"1"}` {
		t.Errorf("execution input = %s, want {\"attempt\":\"1\"}", described.Input)
	}
}
