package identity

import "github.com/sustinbebustin/understudy/internal/aws"

// SNSRoleSession is the session SNS shows in the SenderId of a message it
// delivers to SQS (sqs/R16/01-sqs-enveloped.json).
const SNSRoleSession = "AWS-CLOUDCAST"

// serviceRoleID is the id of the role a service delivers as in partition:
// AROA and 17 upper-case letters and digits, as IAM mints them. The role is
// AWS's own, so the id is fixed, not drawn from a seeded stream, and seeded
// runs keep their draws. AWS's value is unrecorded: only its form is, so
// the id is inferred.
// understudy serves only the aws partition, so one id serves every
// partition until another is served.
func serviceRoleID(_ aws.Partition) string { return "AROAUNDERSTUDYSVCROLE" }

// ServiceSenderID is the SenderId an AWS service's delivery to SQS carries:
// <role id>:<session>, where session is the service's role session name,
// such as SNSRoleSession. SQS shows it in place of the service principal.
func ServiceSenderID(partition aws.Partition, session string) string {
	return serviceRoleID(partition) + ":" + session
}
