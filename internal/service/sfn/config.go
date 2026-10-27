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
// ARN. AWS's code and text for a malformed roleArn are unrecorded
// (inferred, SFN-08); understudy answers InvalidArn in the recorded
// "Invalid Arn" forms, in the order of stepfunctions-api.md 13.3: not an
// IAM ARN is "Invalid ARN prefix", an IAM ARN of another resource type is
// "Resource type not valid in this context".
func checkRoleArn(raw string) *awserr.Error {
	a, err := aws.ParseARN(raw)
	if err != nil || a.Service != "iam" || a.Account == "" || a.Region != "" {
		return sfnapi.ErrInvalidArn("Invalid Arn: 'Invalid ARN prefix: " + raw + "'")
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
		found, err := s.logGroupExists(ctx, c, *d.CloudWatchLogsLogGroup.LogGroupArn)
		if err != nil {
			return err
		}
		if !found {
			return sfnapi.ErrInvalidLoggingConfiguration(errDestinationNotFound)
		}
	}
	return nil
}

// logGroupExists reads the log group a destination ARN names, with or
// without the trailing ":*" DescribeLogGroups gives (an ARN without it is
// accepted and echoed as sent: inferred, SFN-07). The lookup runs as the
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
