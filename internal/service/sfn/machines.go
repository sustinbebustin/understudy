package sfn

import (
	"context"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
	"github.com/sustinbebustin/understudy/internal/store"
)

// unsupportedSuffix ends every 501 refusal of a member.
const unsupportedSuffix = " Run 'understudy coverage --service sfn' for what is supported."

// checkUnimplemented refuses the members of a Standard state machine
// configuration understudy does not implement, naming the member as
// Lambda's refusals do: type EXPRESS, publish true and a
// CUSTOMER_MANAGED_KMS_KEY encryptionConfiguration (spec "Definition
// grammar"). Storing any of them without effect would fake an Express
// workflow, a version or encrypted execution data.
func checkUnimplemented(op string, typ *sfnapi.StateMachineType, publish bool, enc *sfnapi.EncryptionConfiguration) *awserr.Error {
	switch {
	case typ != nil && *typ == sfnapi.StateMachineTypeExpress:
		return sfnapi.ErrNotImplemented(op + " with type EXPRESS asks for an Express workflow, which understudy does not implement; nothing was changed. " +
			"Leave type out or set it to STANDARD." + unsupportedSuffix)
	case publish:
		return sfnapi.ErrNotImplemented(op + " with publish true publishes a state machine version, which understudy does not implement; nothing was changed. " +
			"Leave publish out or set it to false." + unsupportedSuffix)
	case enc != nil && enc.Type == sfnapi.EncryptionTypeCustomerManagedKmsKey:
		return sfnapi.ErrNotImplemented(op + " with encryptionConfiguration CUSTOMER_MANAGED_KMS_KEY encrypts execution data with a KMS key, which understudy does not implement; nothing was changed. " +
			"Leave encryptionConfiguration out or set its type to AWS_OWNED_KEY." + unsupportedSuffix)
	}
	return nil
}

// checkDefinition parses definition with the features whose runtime has
// landed and answers the first error, as CreateStateMachine renders it
// (UNVERIFIED, ASL-01, SFN-08).
func checkDefinition(definition string) *awserr.Error {
	if _, diags := asl.Parse(definition, AcceptedFeatures()); len(diags) > 0 {
		for _, d := range diags {
			if d.Severity == asl.SeverityError {
				return sfnapi.ErrInvalidDefinition(d.InvalidDefinition())
			}
		}
	}
	return nil
}

func checkCreate(in *sfnapi.CreateStateMachineInput) *awserr.Error {
	if err := checkUnimplemented("CreateStateMachine", in.Type, in.Publish, in.EncryptionConfiguration); err != nil {
		return err
	}
	if in.VersionDescription != nil {
		// Recorded (stepfunctions-api.md 5.1).
		return sfnapi.ErrValidationException("Version description can only be set when publish is true")
	}
	if !validName(in.Name) {
		return errInvalidName(in.Name)
	}
	if len(in.Tags) > maxTags {
		return errTooManyTags(in.Name)
	}
	return checkDefinition(in.Definition)
}

// createStateMachine creates a Standard state machine. A request equal in
// name, definition, type and configuration answers the existing machine,
// keeping its role and tags; any other request for the name answers
// StateMachineAlreadyExists (stepfunctions-api.md 5.1).
func (s *Service) createStateMachine(_ context.Context, c *kernel.Call, in *sfnapi.CreateStateMachineInput) (*sfnapi.CreateStateMachineOutput, *awserr.Error) {
	if err := checkCreate(in); err != nil {
		return nil, err
	}
	ref := machineRef{scope: c.Scope(), name: in.Name}
	m := Machine{
		Name:       in.Name,
		Definition: in.Definition,
		RoleArn:    in.RoleArn,
		Logging:    loggingOf(in.LoggingConfiguration),
		Tracing:    in.TracingConfiguration != nil && in.TracingConfiguration.Enabled,
		Tags:       tagsOf(nil, in.Tags),
	}
	created, err := kernel.Write(c, s.cells, ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (clock.Instant, *awserr.Error) {
		if old, ok := st.Machines[ref.name]; ok {
			if old.Definition != m.Definition || !slices.Equal(old.Logging.Destinations, m.Logging.Destinations) ||
				old.Logging.Level != m.Logging.Level || old.Logging.IncludeExecutionData != m.Logging.IncludeExecutionData || old.Tracing != m.Tracing {
				return clock.Instant{}, sfnapi.ErrStateMachineAlreadyExists("State Machine Already Exists: '" + ref.arn().String() + "'")
			}
			tx.Unchanged()
			return old.Created, nil
		}
		m.Created = now
		st.Machines[ref.name] = m
		return now, nil
	})
	if err != nil {
		return nil, err
	}
	return &sfnapi.CreateStateMachineOutput{StateMachineArn: ref.arn().String(), CreationDate: created.Wire()}, nil
}

// machine reads the machine an ARN names; StateMachineDoesNotExist names
// the ARN as sent (stepfunctions-api.md 3.2).
func (s *Service) machine(c *kernel.Call, raw string) (machineRef, Machine, *awserr.Error) {
	ref, ok, err := machineOfARN(c.Scope(), raw)
	if err != nil {
		return machineRef{}, Machine{}, err
	}
	missing := sfnapi.ErrStateMachineDoesNotExist("State Machine Does Not Exist: '" + raw + "'")
	if !ok {
		return machineRef{}, Machine{}, missing
	}
	cell, inCell := s.cells.Lookup(ref.scope)
	if !inCell {
		return machineRef{}, Machine{}, missing
	}
	got := store.Read(cell, func(st *State) found[Machine] {
		m, ok := st.Machines[ref.name]
		m.Tags = slices.Clone(m.Tags)
		m.Logging.Destinations = slices.Clone(m.Logging.Destinations)
		return found[Machine]{value: m, ok: ok}
	})
	if !got.ok {
		return machineRef{}, Machine{}, missing
	}
	return ref, got.value, nil
}

// found is a value read out of a cell and whether it was there.
type found[T any] struct {
	value T
	ok    bool
}

func (s *Service) describeStateMachine(_ context.Context, c *kernel.Call, in *sfnapi.DescribeStateMachineInput) (*sfnapi.DescribeStateMachineOutput, *awserr.Error) {
	ref, m, err := s.machine(c, in.StateMachineArn)
	if err != nil {
		return nil, err
	}
	status := sfnapi.StateMachineStatusActive
	return &sfnapi.DescribeStateMachineOutput{
		StateMachineArn:         ref.arn().String(),
		Name:                    m.Name,
		Status:                  &status,
		Definition:              m.Definition,
		RoleArn:                 m.RoleArn,
		Type:                    sfnapi.StateMachineTypeStandard,
		CreationDate:            m.Created.Wire(),
		LoggingConfiguration:    m.Logging.wire(),
		TracingConfiguration:    &sfnapi.TracingConfiguration{Enabled: m.Tracing},
		EncryptionConfiguration: awsOwnedKey(),
	}, nil
}

// awsOwnedKey is the encryptionConfiguration every machine describes with:
// CUSTOMER_MANAGED_KMS_KEY is refused (stepfunctions-api.md 5.2).
func awsOwnedKey() *sfnapi.EncryptionConfiguration {
	return &sfnapi.EncryptionConfiguration{Type: sfnapi.EncryptionTypeAwsOwnedKey}
}

// deleteStateMachine removes the machine. Deleting one that does not exist
// answers 200 (stepfunctions-api.md 5.4). Its executions stay, and a
// running one runs on to its end.
func (s *Service) deleteStateMachine(_ context.Context, c *kernel.Call, in *sfnapi.DeleteStateMachineInput) (*sfnapi.DeleteStateMachineOutput, *awserr.Error) {
	ref, ok, err := machineOfARN(c.Scope(), in.StateMachineArn)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &sfnapi.DeleteStateMachineOutput{}, nil
	}
	_, err = kernel.Write(c, s.cells, ref.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.Machines[ref.name]; !ok {
			tx.Unchanged()
			return struct{}{}, nil
		}
		delete(st.Machines, ref.name)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sfnapi.DeleteStateMachineOutput{}, nil
}

// listStateMachines lists the machines by name as a string
// (stepfunctions-api.md 5.5).
func (s *Service) listStateMachines(_ context.Context, c *kernel.Call, in *sfnapi.ListStateMachinesInput) (*sfnapi.ListStateMachinesOutput, *awserr.Error) {
	scope := c.Scope()
	listing := "ListStateMachines\x00" + machineRef{scope: scope}.arn().String()
	after := ""
	if in.NextToken != nil {
		var err *awserr.Error
		if after, err = readToken(*in.NextToken, listing, c.Admitted()); err != nil {
			return nil, err
		}
	}
	var machines []Machine
	if cell, ok := s.cells.Lookup(scope); ok {
		machines = store.Read(cell, func(st *State) []Machine {
			out := make([]Machine, 0, len(st.Machines))
			for _, name := range slices.Sorted(maps.Keys(st.Machines)) {
				if in.NextToken == nil || name > after {
					out = append(out, st.Machines[name])
				}
			}
			return out
		})
	}
	out := &sfnapi.ListStateMachinesOutput{StateMachines: []sfnapi.StateMachineListItem{}}
	size := pageSize(in.MaxResults)
	for i := range machines {
		if i == size {
			out.NextToken = new(pageToken(listing, machines[i-1].Name, c.Admitted()))
			break
		}
		m := &machines[i]
		out.StateMachines = append(out.StateMachines, sfnapi.StateMachineListItem{
			StateMachineArn: machineRef{scope: scope, name: m.Name}.arn().String(),
			Name:            m.Name,
			Type:            sfnapi.StateMachineTypeStandard,
			CreationDate:    m.Created.Wire(),
		})
	}
	return out, nil
}

// listStateMachineVersions lists no version: publishing is refused.
func (s *Service) listStateMachineVersions(_ context.Context, c *kernel.Call, in *sfnapi.ListStateMachineVersionsInput) (*sfnapi.ListStateMachineVersionsOutput, *awserr.Error) {
	if _, _, err := s.machine(c, in.StateMachineArn); err != nil {
		return nil, err
	}
	return &sfnapi.ListStateMachineVersionsOutput{StateMachineVersions: []sfnapi.StateMachineVersionListItem{}}, nil
}

// maxDiagnostics is the diagnostics of a maxResults of 0
// (stepfunctions-asl.md 3.6).
const maxDiagnostics = 100

// validateStateMachineDefinition answers 200 with the diagnostics of the
// definition, never an error for a bad one (stepfunctions-api.md 5.6).
func (s *Service) validateStateMachineDefinition(_ context.Context, _ *kernel.Call, in *sfnapi.ValidateStateMachineDefinitionInput) (*sfnapi.ValidateStateMachineDefinitionOutput, *awserr.Error) {
	if err := checkUnimplemented("ValidateStateMachineDefinition", in.Type, false, nil); err != nil {
		return nil, err
	}
	_, diags := asl.Parse(in.Definition, AcceptedFeatures())
	out := &sfnapi.ValidateStateMachineDefinitionOutput{
		Result:      sfnapi.ValidateStateMachineDefinitionResultCodeOk,
		Diagnostics: []sfnapi.ValidateStateMachineDefinitionDiagnostic{},
		Truncated:   new(false),
	}
	warnings := in.Severity != nil && *in.Severity == sfnapi.ValidateStateMachineDefinitionSeverityWarning
	limit := int(in.MaxResults)
	if limit == 0 {
		limit = maxDiagnostics
	}
	for _, d := range diags {
		if d.Severity == asl.SeverityError {
			out.Result = sfnapi.ValidateStateMachineDefinitionResultCodeFail
		}
		if d.Severity != asl.SeverityError && !warnings {
			continue
		}
		if len(out.Diagnostics) == limit {
			out.Truncated = new(true)
			continue
		}
		w := sfnapi.ValidateStateMachineDefinitionDiagnostic{
			Severity: sfnapi.ValidateStateMachineDefinitionSeverity(d.Severity.String()),
			Code:     d.Code,
			Message:  d.Message,
		}
		if d.Location != "" {
			w.Location = new(d.Location)
		}
		out.Diagnostics = append(out.Diagnostics, w)
	}
	return out, nil
}

// loggingOf is the stored form of a loggingConfiguration; an absent or
// empty one is the default (stepfunctions-api.md 5.1).
func loggingOf(in *sfnapi.LoggingConfiguration) Logging {
	if in == nil {
		return Logging{}
	}
	l := Logging{IncludeExecutionData: in.IncludeExecutionData}
	if in.Level != nil && *in.Level != sfnapi.LogLevelOff {
		l.Level = string(*in.Level)
	}
	for _, d := range in.Destinations {
		arn := ""
		if d.CloudWatchLogsLogGroup != nil && d.CloudWatchLogsLogGroup.LogGroupArn != nil {
			arn = *d.CloudWatchLogsLogGroup.LogGroupArn
		}
		l.Destinations = append(l.Destinations, arn)
	}
	return l
}

// wire is the loggingConfiguration a description echoes.
func (l Logging) wire() *sfnapi.LoggingConfiguration {
	level := sfnapi.LogLevelOff
	if l.Level != "" {
		level = sfnapi.LogLevel(l.Level)
	}
	out := &sfnapi.LoggingConfiguration{Level: &level, IncludeExecutionData: l.IncludeExecutionData}
	for _, arn := range l.Destinations {
		d := sfnapi.LogDestination{}
		if arn != "" {
			d.CloudWatchLogsLogGroup = &sfnapi.CloudWatchLogsLogGroup{LogGroupArn: new(arn)}
		}
		out.Destinations = append(out.Destinations, d)
	}
	return out
}
