package sfn

import (
	"context"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// statesPrincipal is the service principal Step Functions acts as.
const statesPrincipal = "states.amazonaws.com"

// checkRoleArn checks the execution role ARN's syntax only: an IAM role
// ARN. A value that is not an IAM ARN answers InvalidArn "Invalid Role
// Arn: '<arn>'", recorded for a value that is no ARN at all (SFN-08,
// recorded 2026-11-03; stepfunctions-api.md 5.1) and inferred for the
// rest; an IAM ARN of another resource type answers the inferred "Resource
// type not valid in this context".
func checkRoleArn(raw string) *awserr.Error {
	a, err := aws.ParseARN(raw)
	if err != nil || a.Service != "iam" || a.Account == "" || a.Region != "" {
		return sfnapi.ErrInvalidArn("Invalid Role Arn: '" + raw + "'")
	}
	typ, name, _ := strings.Cut(a.Resource, "/")
	if typ != "role" || name == "" {
		return sfnapi.ErrInvalidArn("Invalid Arn: 'Resource type not valid in this context: " + typ + "'")
	}
	return nil
}

// The recorded InvalidLoggingConfiguration texts (stepfunctions-api.md 5.1;
// localstack@8b9a79f v2/test_sfn_api_logs.snapshot.json:L509-L552).
const (
	errOneDestination      = "Invalid Logging Configuration: Must specify exactly one Log Destination."
	errDestinationNotFound = "Invalid Logging Configuration: Log Destination not found."
	// errNoQualifier is recorded for a log group ARN without ":*" (SFN-07,
	// 2026-11-03).
	errNoQualifier = "Invalid Logging Configuration: Log Group ARN must be provided with '*' qualifier."
)

// checkLogging validates a loggingConfiguration (stepfunctions-api.md 5.1):
// a level other than OFF needs exactly one destination, and every
// destination must name a log group that exists. It calls CloudWatch Logs,
// so it runs before the handler takes its cell lock.
func (s *Service) checkLogging(ctx context.Context, c *kernel.Call, in *sfnapi.LoggingConfiguration) *awserr.Error {
	if in == nil {
		return nil
	}
	if in.Level != nil && *in.Level != sfnapi.LogLevelOff && len(in.Destinations) != 1 {
		return sfnapi.ErrInvalidLoggingConfiguration(errOneDestination)
	}
	for _, d := range in.Destinations {
		if d.CloudWatchLogsLogGroup == nil || d.CloudWatchLogsLogGroup.LogGroupArn == nil {
			return sfnapi.ErrInvalidLoggingConfiguration(errDestinationNotFound)
		}
		raw := *d.CloudWatchLogsLogGroup.LogGroupArn
		if a, err := aws.ParseARN(raw); err == nil && a.Service == "logs" && strings.HasPrefix(a.Resource, "log-group:") && !strings.HasSuffix(a.Resource, ":*") {
			// Checked before the group's existence (inferred: the recording's
			// group existed).
			return sfnapi.ErrInvalidLoggingConfiguration(errNoQualifier)
		}
		found, err := s.logGroupExists(ctx, c, raw)
		if err != nil {
			return err
		}
		if !found {
			return sfnapi.ErrInvalidLoggingConfiguration(errDestinationNotFound)
		}
	}
	return nil
}

// logGroupExists reads the log group a destination ARN names, less the
// trailing ":*" checkLogging requires. The lookup runs as the
// platform: it is not access-recorded, since the permissions log delivery
// needs are the caller's and the role's, not this read's
// (stepfunctions-api.md 9.5). A value that names no log group is not found;
// only Logs' not-found answer means the group does not exist, and any other
// failure is the call's error.
func (s *Service) logGroupExists(ctx context.Context, c *kernel.Call, raw string) (bool, *awserr.Error) {
	a, err := aws.ParseARN(raw)
	if err != nil || a.Service != "logs" {
		return false, nil
	}
	name, ok := strings.CutPrefix(a.Resource, "log-group:")
	name = strings.TrimSuffix(name, ":*")
	if !ok || name == "" {
		return false, nil
	}
	scope, err := aws.ScopeOf(a)
	if err != nil {
		return false, nil
	}
	t := kernel.Target{Scope: scope, As: kernel.AsPlatform{Name: statesPrincipal}, Via: ref.SFNLogDestination}
	_, aerr := kernel.Invoke(ctx, s.k, c, logsapi.OpDescribeLogStreams, t, &logsapi.DescribeLogStreamsInput{LogGroupName: &name, Limit: new(int32(1))})
	switch {
	case aerr == nil:
		return true, nil
	case aerr.ID == logsapi.IdentityResourceNotFoundException():
		return false, nil
	}
	return false, aerr
}
