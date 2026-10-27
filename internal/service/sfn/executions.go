package sfn

import (
	"context"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
	"github.com/sustinbebustin/understudy/internal/store"
)

// startedExecution is what StartExecution answers.
type startedExecution struct {
	arn     string
	started clock.Instant
}

// executionInput parses StartExecution's input: omitted is {}
// (stepfunctions-api.md 6.1). The empty string answers the recorded text;
// any other input that is not JSON answers it too, where AWS answers its
// parser's text (inferred, SFN-08).
func executionInput(in *string) (string, document.Value, *awserr.Error) {
	if in == nil {
		return "{}", document.Map{}, nil
	}
	invalid := sfnapi.ErrInvalidExecutionInput("Invalid State Machine Execution Input: 'Invalid execution input.'")
	if *in == "" {
		return "", nil, invalid
	}
	d := jsonx.NewDecoder([]byte(*in))
	v, err := d.ReadValue()
	if err == nil {
		err = d.End()
	}
	if err != nil {
		return "", nil, invalid
	}
	return *in, v, nil
}

// startExecution starts a Standard execution. The same name and input as a
// running execution answers that execution; a closed one, or other input,
// answers ExecutionAlreadyExists (stepfunctions-api.md 6.1). Input is
// compared byte for byte (inferred, SFN-03).
func (s *Service) startExecution(_ context.Context, c *kernel.Call, in *sfnapi.StartExecutionInput) (*sfnapi.StartExecutionOutput, *awserr.Error) {
	machine, ok, err := machineOfARN(c.Scope(), in.StateMachineArn)
	if err != nil {
		return nil, err
	}
	missing := sfnapi.ErrStateMachineDoesNotExist("State Machine Does Not Exist: '" + in.StateMachineArn + "'")
	if !ok {
		return nil, missing
	}
	name := deref(in.Name)
	if in.Name == nil {
		name = s.ids.UUID(ids.KindSFNExecutionName).String()
	}
	if !validName(name) {
		return nil, errInvalidName(name)
	}
	raw, input, err := executionInput(in.Input)
	if err != nil {
		return nil, err
	}
	trace := deref(in.TraceHeader)
	if h := c.Trace(); h != "" {
		trace = h
	}
	ref := executionRef{machine: machine, name: name}
	started, err := kernel.Write(c, s.cells, machine.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (startedExecution, *awserr.Error) {
		m, ok := st.Machines[machine.name]
		if !ok {
			return startedExecution{}, missing
		}
		if old, ok := st.Executions[ref.key()]; ok {
			if _, done := old.Engine.Outcome(); done || old.Input != raw {
				return startedExecution{}, sfnapi.ErrExecutionAlreadyExists("Execution Already Exists: '" + ref.arn().String() + "'")
			}
			tx.Unchanged()
			return startedExecution{arn: ref.arn().String(), started: old.Started}, nil
		}
		def, diags := asl.Parse(m.Definition, AcceptedFeatures())
		if def == nil {
			return startedExecution{}, definitionError(diags)
		}
		m.Tags = slices.Clone(m.Tags)
		rec := Execution{Machine: m.Name, Name: name, Input: raw, TraceHeader: trace, Started: now, Run: m}
		start := asl.Start{At: now.Wire(), Input: input, Context: startContext(ref, m.RoleArn, input, now)}
		x, events, effects := asl.Step(def, EngineRules(), intrinsicSource{s.ids}, asl.Execution{}, start)
		s.record(tx, ref, &rec, x, events, effects, now)
		st.Executions[ref.key()] = rec
		return startedExecution{arn: ref.arn().String(), started: now}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sfnapi.StartExecutionOutput{ExecutionArn: started.arn, StartDate: started.started.Wire()}, nil
}

// definitionError answers the first error of a stored definition that no
// longer parses, as CreateStateMachine would.
func definitionError(diags []asl.Diagnostic) *awserr.Error {
	for _, d := range diags {
		if d.Severity == asl.SeverityError {
			return sfnapi.ErrInvalidDefinition(d.InvalidDefinition())
		}
	}
	return sfnapi.ErrInvalidDefinition("Invalid State Machine Definition: the stored definition no longer parses")
}

// startContext is the context object's Execution and StateMachine members
// (stepfunctions-asl.md 4.7).
func startContext(ref executionRef, role string, input document.Value, now clock.Instant) document.Map {
	return document.Map{
		{Key: "Execution", Value: document.Map{
			{Key: "Id", Value: document.String(ref.arn().String())},
			{Key: "Input", Value: input},
			{Key: "Name", Value: document.String(ref.name)},
			{Key: "RoleArn", Value: document.String(role)},
			{Key: "StartTime", Value: document.String(asl.ContextTime(now.Wire()))},
			{Key: "RedriveCount", Value: document.Number("0")},
		}},
		{Key: "StateMachine", Value: document.Map{
			{Key: "Id", Value: document.String(ref.machine.arn().String())},
			{Key: "Name", Value: document.String(ref.machine.name)},
		}},
	}
}

// execution reads the execution an ARN names; ExecutionDoesNotExist names
// the ARN as sent (stepfunctions-api.md 3.2).
func (s *Service) execution(c *kernel.Call, raw string) (executionRef, Execution, *awserr.Error) {
	ref, ok, err := executionOfARN(c.Scope(), raw)
	if err != nil {
		return executionRef{}, Execution{}, err
	}
	missing := sfnapi.ErrExecutionDoesNotExist("Execution Does Not Exist: '" + raw + "'")
	if !ok {
		return executionRef{}, Execution{}, missing
	}
	cell, inCell := s.cells.Lookup(ref.machine.scope)
	if !inCell {
		return executionRef{}, Execution{}, missing
	}
	got := store.Read(cell, func(st *State) found[Execution] {
		rec, ok := st.Executions[ref.key()]
		rec.Events = slices.Clone(rec.Events)
		rec.Run.Tags = slices.Clone(rec.Run.Tags)
		rec.Run.Logging.Destinations = slices.Clone(rec.Run.Logging.Destinations)
		return found[Execution]{value: rec, ok: ok}
	})
	if !got.ok {
		return executionRef{}, Execution{}, missing
	}
	return ref, got.value, nil
}

// describeExecution describes an execution. A SUCCEEDED one carries the
// recorded redrive members (stepfunctions-api.md 6.3); others carry
// redriveCount 0 only.
func (s *Service) describeExecution(_ context.Context, c *kernel.Call, in *sfnapi.DescribeExecutionInput) (*sfnapi.DescribeExecutionOutput, *awserr.Error) {
	ref, rec, err := s.execution(c, in.ExecutionArn)
	if err != nil {
		return nil, err
	}
	out := &sfnapi.DescribeExecutionOutput{
		ExecutionArn:    ref.arn().String(),
		StateMachineArn: ref.machine.arn().String(),
		Name:            new(rec.Name),
		Status:          sfnapi.ExecutionStatusRunning,
		StartDate:       rec.Started.Wire(),
		Input:           new(rec.Input),
		InputDetails:    &sfnapi.CloudWatchEventsExecutionDataDetails{Included: true},
		RedriveCount:    new(int32(0)),
	}
	if rec.TraceHeader != "" {
		out.TraceHeader = new(rec.TraceHeader)
	}
	if !rec.Stopped.IsZero() {
		out.StopDate = new(rec.Stopped.Wire())
	}
	outcome, _ := rec.Engine.Outcome()
	switch o := outcome.(type) {
	case asl.Succeeded:
		out.Status = sfnapi.ExecutionStatusSucceeded
		out.Output = new(docText(o.Output))
		out.OutputDetails = &sfnapi.CloudWatchEventsExecutionDataDetails{Included: true}
		out.RedriveStatus = new(sfnapi.ExecutionRedriveStatusNotRedrivable)
		out.RedriveStatusReason = new("Execution is SUCCEEDED and cannot be redriven")
	case asl.Failed:
		out.Status = sfnapi.ExecutionStatusFailed
		out.Error, out.Cause = optional(o.Error), optional(o.Cause)
	case asl.TimedOut:
		out.Status = sfnapi.ExecutionStatusTimedOut
		out.Error, out.Cause = optional(o.Error), optional(o.Cause)
	case asl.Aborted:
		out.Status = sfnapi.ExecutionStatusAborted
		out.Error, out.Cause = optional(o.Error), optional(o.Cause)
	}
	return out, nil
}

// optional is s, or nil when it is empty.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// describeStateMachineForExecution describes the machine as the execution
// ran it, with updateDate in place of creationDate (stepfunctions-api.md
// 6.3): the last effective update before the start, or the creation.
func (s *Service) describeStateMachineForExecution(_ context.Context, c *kernel.Call, in *sfnapi.DescribeStateMachineForExecutionInput) (*sfnapi.DescribeStateMachineForExecutionOutput, *awserr.Error) {
	ref, rec, err := s.execution(c, in.ExecutionArn)
	if err != nil {
		return nil, err
	}
	return &sfnapi.DescribeStateMachineForExecutionOutput{
		StateMachineArn:         ref.machine.arn().String(),
		Name:                    rec.Run.Name,
		Definition:              rec.Run.Definition,
		RoleArn:                 rec.Run.RoleArn,
		UpdateDate:              rec.Run.lastChange().Wire(),
		LoggingConfiguration:    rec.Run.Logging.wire(),
		TracingConfiguration:    &sfnapi.TracingConfiguration{Enabled: rec.Run.Tracing},
		RevisionId:              optional(rec.Run.RevisionID),
		EncryptionConfiguration: awsOwnedKey(),
	}, nil
}

// getExecutionHistory pages the history, oldest first or with
// reverseOrder newest first. includeExecutionData false leaves out input
// and output and keeps the *Details members (inferred, SFN-05).
func (s *Service) getExecutionHistory(_ context.Context, c *kernel.Call, in *sfnapi.GetExecutionHistoryInput) (*sfnapi.GetExecutionHistoryOutput, *awserr.Error) {
	ref, rec, err := s.execution(c, in.ExecutionArn)
	if err != nil {
		return nil, err
	}
	listing := "GetExecutionHistory\x00" + ref.arn().String()
	if in.ReverseOrder {
		listing += "\x00reverse"
	}
	events := rec.Events
	if in.ReverseOrder {
		events = slices.Clone(events)
		slices.Reverse(events)
	}
	from := 0
	if in.NextToken != nil {
		at, err := readToken(*in.NextToken, listing, c.Admitted())
		if err != nil {
			return nil, err
		}
		if from = positionOf(at); from < 0 || from > len(events) {
			return nil, errInvalidToken()
		}
	}
	data := in.IncludeExecutionData == nil || *in.IncludeExecutionData
	size := pageSize(in.MaxResults)
	out := &sfnapi.GetExecutionHistoryOutput{Events: []sfnapi.HistoryEvent{}}
	for i := from; i < len(events); i++ {
		if i-from == size {
			out.NextToken = new(pageToken(listing, positionText(i), c.Admitted()))
			break
		}
		out.Events = append(out.Events, wireEvent(&events[i], data))
	}
	return out, nil
}
