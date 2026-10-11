package dynamodb

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// errCaseTwin refuses a table whose name differs from an existing one only
// in letter case. AWS keeps both; Local would treat them as one table, so
// understudy refuses rather than letting two names share data.
func errCaseTwin(name, twin string) *awserr.Error {
	return dynamodbapi.ErrResourceInUseException("Table already exists: " + twin +
		". understudy runs DynamoDB on DynamoDB Local, which treats table names case-insensitively, so table " + name +
		" cannot exist beside " + twin + " in the same account and region; choose a name that differs in more than letter case.")
}

// errLocalUnavailable answers a call DynamoDB Local could not serve: it did
// not start, or the connection to it failed. Nothing reached Local, or its
// answer was lost, so the client may retry.
func errLocalUnavailable(what string, err error) *awserr.Error {
	return dynamodbapi.ErrInternalServerError("understudy could not " + what + ": " + err.Error() +
		". Other services are unaffected; check `understudy doctor` and the server log, then retry.")
}

// errStillStarting answers a call that waited the whole bound for Local to
// start. The start goes on, so a retry, which every SDK makes on a 500,
// finds it ready.
func errStillStarting(waited time.Duration) *awserr.Error {
	return dynamodbapi.ErrInternalServerError("DynamoDB Local is still being pulled or started after " + waited.String() +
		"; the start goes on in the background, so retry the call. The first start pulls the image, which can take minutes on a slow network; " +
		"the server log records when DynamoDB Local is ready.")
}
