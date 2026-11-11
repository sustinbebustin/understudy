package sfn

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
	"github.com/sustinbebustin/understudy/internal/store"
)

// validateListExecutions checks statusFilter, whose check the overlay hands
// to the service. The violation lists the values in
// AWS's order rather than the model's (stepfunctions-api.md 3.2;
// aws-recorded localstack@8b9a79f v2/test_sfn_api.snapshot.json:L1672).
func validateListExecutions(in *sfnapi.ListExecutionsInput) []awserr.Violation {
	if in.StatusFilter == nil || in.StatusFilter.Known() {
		return nil
	}
	return []awserr.Violation{{
		Path:       awserr.Path{"statusFilter"},
		Value:      awserr.RenderedText{Text: string(*in.StatusFilter)},
		Constraint: awserr.Enum{Values: []string{"SUCCEEDED", "TIMED_OUT", "PENDING_REDRIVE", "ABORTED", "FAILED", "RUNNING"}},
	}}
}

// listedExecution is one execution as ListExecutions orders it.
type listedExecution struct {
	item sfnapi.ExecutionListItem
	// at is the sort instant: the stop date of a closed execution, the
	// start date of a running one (stepfunctions-api.md 6.5).
	at time.Time
}

// listExecutions lists a machine's executions, most recent first: running
// ones by startDate, closed ones by stopDate (stepfunctions-api.md 6.5).
// Ties, which AWS leaves unspecified, go by name. A mapRunArn is checked as
// an ARN and lists nothing, since no Map Run exists; redriveFilter and
// PENDING_REDRIVE apply to Map Runs only (aws-doc api/API_ListExecutions).
func (s *Service) listExecutions(_ context.Context, c *kernel.Call, in *sfnapi.ListExecutionsInput) (*sfnapi.ListExecutionsOutput, *awserr.Error) {
	if err := checkListExecutions(in); err != nil {
		return nil, err
	}
	empty := &sfnapi.ListExecutionsOutput{Executions: []sfnapi.ExecutionListItem{}}
	if in.MapRunArn != nil {
		if _, _, err := statesResource(c.Scope(), *in.MapRunArn, "mapRun"); err != nil {
			return nil, err
		}
		return empty, nil
	}
	ref, all, err := s.machineExecutions(c, smithy.Deref(in.StateMachineArn), in.StatusFilter)
	if err != nil {
		return nil, err
	}
	listing := "ListExecutions\x00" + ref.arn().String()
	if in.StatusFilter != nil {
		listing += "\x00" + string(*in.StatusFilter)
	}
	from := 0
	if in.NextToken != nil {
		position, err := readToken(*in.NextToken, listing, c.Admitted())
		if err != nil {
			return nil, err
		}
		after, ok := cursorOf(position)
		if !ok {
			return nil, errInvalidToken()
		}
		from = slices.IndexFunc(all, after.before)
		if from < 0 {
			from = len(all)
		}
	}
	out := empty
	size := pageSize(in.MaxResults)
	for i := from; i < len(all); i++ {
		if i-from == size {
			out.NextToken = new(pageToken(listing, cursorText(all[i-1]), c.Admitted()))
			break
		}
		out.Executions = append(out.Executions, all[i].item)
	}
	return out, nil
}

// machineExecutions are the executions of the machine raw names with the
// status filter, if any, in ListExecutions order.
func (s *Service) machineExecutions(c *kernel.Call, raw string, status *sfnapi.ExecutionStatus) (machineRef, []listedExecution, *awserr.Error) {
	ref, ok, err := machineOfARN(c.Scope(), raw)
	if err != nil {
		return machineRef{}, nil, err
	}
	missing := sfnapi.ErrStateMachineDoesNotExist("State Machine Does Not Exist: '" + raw + "'")
	cell, inCell := s.cells.Lookup(ref.scope)
	if !ok || !inCell {
		return machineRef{}, nil, missing
	}
	got := store.Read(cell, func(st *State) found[[]listedExecution] {
		if _, ok := st.Machines[ref.name]; !ok {
			return found[[]listedExecution]{}
		}
		var out []listedExecution
		for key := range st.Executions {
			rec := st.Executions[key]
			if rec.Machine != ref.name {
				continue
			}
			l := listed(executionRef{machine: ref, name: rec.Name}, &rec)
			if status == nil || l.item.Status == *status {
				out = append(out, l)
			}
		}
		return found[[]listedExecution]{value: out, ok: true}
	})
	if !got.ok {
		return machineRef{}, nil, missing
	}
	slices.SortFunc(got.value, func(a, b listedExecution) int {
		return cmp.Or(b.at.Compare(a.at), strings.Compare(a.item.Name, b.item.Name))
	})
	return ref, got.value, nil
}

// checkListExecutions applies the documented member rules (aws-doc
// api/API_ListExecutions): exactly one of stateMachineArn and mapRunArn,
// and redriveFilter or a PENDING_REDRIVE statusFilter only with a mapRunArn.
// AWS's texts are unrecorded; these are understudy's.
func checkListExecutions(in *sfnapi.ListExecutionsInput) *awserr.Error {
	switch {
	case in.StateMachineArn == nil && in.MapRunArn == nil:
		return sfnapi.ErrValidationException("Must provide a StateMachine ARN or MapRun ARN")
	case in.StateMachineArn != nil && in.MapRunArn != nil:
		return sfnapi.ErrValidationException("Cannot provide both a StateMachine ARN and a MapRun ARN")
	case in.MapRunArn != nil:
		return nil
	case in.RedriveFilter != nil:
		return sfnapi.ErrValidationException("redriveFilter can only be used with a MapRun ARN")
	case in.StatusFilter != nil && *in.StatusFilter == sfnapi.ExecutionStatusPendingRedrive:
		return sfnapi.ErrValidationException("statusFilter PENDING_REDRIVE can only be used with a MapRun ARN")
	}
	return nil
}

// listed is rec as a ListExecutions item, with its sort instant.
func listed(ref executionRef, rec *Execution) listedExecution {
	item := sfnapi.ExecutionListItem{
		ExecutionArn:    ref.arn().String(),
		StateMachineArn: ref.machine.arn().String(),
		Name:            rec.Name,
		Status:          statusOf(rec.Engine),
		StartDate:       rec.Started.Wire(),
		RedriveCount:    new(int32(0)),
	}
	at := item.StartDate
	if !rec.Stopped.IsZero() {
		item.StopDate = new(rec.Stopped.Wire())
		at = *item.StopDate
	}
	return listedExecution{item: item, at: at}
}

// statusOf is the ExecutionStatus of an execution's engine state.
func statusOf(x asl.Execution) sfnapi.ExecutionStatus {
	outcome, _ := x.Outcome()
	switch outcome.(type) {
	case asl.Succeeded:
		return sfnapi.ExecutionStatusSucceeded
	case asl.Failed:
		return sfnapi.ExecutionStatusFailed
	case asl.TimedOut:
		return sfnapi.ExecutionStatusTimedOut
	case asl.Aborted:
		return sfnapi.ExecutionStatusAborted
	}
	return sfnapi.ExecutionStatusRunning
}

// cursor is the last item of a ListExecutions page: its sort instant and
// name.
type cursor struct {
	at   time.Time
	name string
}

// before reports whether the cursor's item sorts before l, so l belongs to
// the next page.
func (c cursor) before(l listedExecution) bool {
	if d := l.at.Compare(c.at); d != 0 {
		return d < 0
	}
	return l.item.Name > c.name
}

func cursorText(l listedExecution) string {
	return strconv.FormatInt(l.at.UnixNano(), 10) + "/" + l.item.Name
}

func cursorOf(s string) (cursor, bool) {
	ns, name, found := strings.Cut(s, "/")
	n, err := strconv.ParseInt(ns, 10, 64)
	if !found || err != nil {
		return cursor{}, false
	}
	return cursor{at: time.Unix(0, n).UTC(), name: name}, true
}
