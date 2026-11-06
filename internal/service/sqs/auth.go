package sqs

import (
	"net/http"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// JSONAuth is SQS's auth vocabulary over awsJson1_0, which the edge uses in
// place of the JSON family's (edge.Options.ProtocolAuth). It is the family's,
// except that a request outside the 15-minute window is refused with 403,
// as SQS answers it, where DynamoDB answers the same
// InvalidSignatureException with 400
// (testdata/parity/sqs/R18/03-sqs-json-skew-16min.json,
// testdata/parity/dynamodb/R18/03-dynamodb-skew-16min.json). SQS's status
// for its other JSON auth failures, which share that identity, is
// unrecorded, so they keep the family's 400. Over awsQuery SQS answers in
// the Query family's words, 403 already.
func JSONAuth() auth.Vocabulary {
	v, _ := auth.VocabularyFor(awserr.JSONFamily)
	v.Skewed.Status = http.StatusForbidden
	return v
}
