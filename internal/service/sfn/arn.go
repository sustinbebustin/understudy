package sfn

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// machineRef locates a state machine: its scope and name.
type machineRef struct {
	scope aws.Scope
	name  string
}

// arn is the machine's ARN (stepfunctions-api.md 4.1).
func (m machineRef) arn() aws.ARN {
	return aws.ARN{
		Partition: m.scope.Partition.String(),
		Service:   "states",
		Region:    m.scope.Region.String(),
		Account:   m.scope.Account.String(),
		Resource:  "stateMachine:" + m.name,
	}
}

// executionRef locates an execution: its machine and name.
type executionRef struct {
	machine machineRef
	name    string
}

// arn is the execution's ARN (stepfunctions-api.md 4.1).
func (e executionRef) arn() aws.ARN {
	a := e.machine.arn()
	a.Resource = "execution:" + e.machine.name + ":" + e.name
	return a
}

// key keys the execution in State.Executions.
func (e executionRef) key() string { return e.machine.name + ":" + e.name }

// statesResource splits a Step Functions ARN of scope's Region into its
// resource type and the rest of its resource (stepfunctions-api.md 3.2):
// a value that is not an ARN is "Invalid ARN prefix"; an ARN whose resource
// has no "type:" part, whatever its service, is "Arn missing resource
// component" (SFN-01, recorded 2026-11-03); an ARN of another service is
// "Invalid ARN prefix"; a resource of another type than want is "Resource
// type not valid in this context"; an ARN of another Region is "Expected
// the ARN ... to be within region" (SFN-01, recorded 2026-11-03).
func statesResource(scope aws.Scope, raw, want string) (aws.ARN, string, *awserr.Error) {
	a, err := aws.ParseARN(raw)
	if err != nil {
		return aws.ARN{}, "", sfnapi.ErrInvalidArn("Invalid Arn: 'Invalid ARN prefix: " + raw + "'")
	}
	typ, rest, typed := strings.Cut(a.Resource, ":")
	switch {
	case !typed:
		return aws.ARN{}, "", sfnapi.ErrInvalidArn("Invalid Arn: 'Arn missing resource component: " + raw + "'")
	case a.Service != "states":
		return aws.ARN{}, "", sfnapi.ErrInvalidArn("Invalid Arn: 'Invalid ARN prefix: " + raw + "'")
	case typ != want:
		return aws.ARN{}, "", sfnapi.ErrInvalidArn("Invalid Arn: 'Resource type not valid in this context: " + typ + "'")
	case a.Region != scope.Region.String():
		return aws.ARN{}, "", sfnapi.ErrInvalidArn("Invalid Arn: 'Expected the ARN " + raw + " to be within region (" + scope.Region.String() + ").'")
	}
	return a, rest, nil
}

// machineOfARN reads a state machine ARN. ok is false for an ARN of another
// account than scope, or a qualified (version or alias) ARN, neither of
// which names a machine of scope.
func machineOfARN(scope aws.Scope, raw string) (machineRef, bool, *awserr.Error) {
	a, name, err := statesResource(scope, raw, "stateMachine")
	if err != nil {
		return machineRef{}, false, err
	}
	if !inScope(scope, a) || name == "" || strings.Contains(name, ":") {
		return machineRef{}, false, nil
	}
	return machineRef{scope: scope, name: name}, true, nil
}

// executionOfARN reads an execution ARN; ok is false as for machineOfARN.
func executionOfARN(scope aws.Scope, raw string) (executionRef, bool, *awserr.Error) {
	a, rest, err := statesResource(scope, raw, "execution")
	if err != nil {
		return executionRef{}, false, err
	}
	machine, name, found := strings.Cut(rest, ":")
	if !inScope(scope, a) || !found || machine == "" || name == "" || strings.Contains(name, ":") {
		return executionRef{}, false, nil
	}
	return executionRef{machine: machineRef{scope: scope, name: machine}, name: name}, true, nil
}

func inScope(scope aws.Scope, a aws.ARN) bool {
	return a.Partition == scope.Partition.String() && a.Region == scope.Region.String() && a.Account == scope.Account.String()
}

// validName applies the character rules of state machine and execution
// names (stepfunctions-api.md 4.2): no whitespace, none of
// ? * < > { } [ ] " # % \ ^ | ~ ` $ & , ; : /, no control characters, no
// U+FFFE, U+FFFF, surrogates or U+10FFFF. The 1 to 80 length is the
// model's.
func validName(name string) bool {
	if !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		switch {
		case r <= 0x20, r >= 0x7f && r <= 0x9f, r == 0xfffe, r == 0xffff, r == 0x10ffff:
			return false
		case strings.ContainsRune("?*<>{}[]\"#%\\^|~`$&,;:/", r), unicode.IsSpace(r):
			return false
		}
	}
	return true
}

// errInvalidName is the recorded InvalidName text (stepfunctions-api.md
// 4.2).
func errInvalidName(name string) *awserr.Error {
	return sfnapi.ErrInvalidName("Invalid Name: '" + name + "'")
}
