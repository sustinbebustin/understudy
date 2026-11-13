package api_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// TestApiGatewayV2ErrorsWriteTheLowercaseMessageKey: AWS carries the text of
// an HTTP API control-plane error under "message", not "Message", for each of
// the four modeled shapes (testdata/parity/apigatewayv2/APIGW2-01/01,
// 08 and 22, APIGW2-15/06, aws-recorded).
func TestApiGatewayV2ErrorsWriteTheLowercaseMessageKey(t *testing.T) {
	for name, id := range map[string]awserr.Identity{
		"AccessDeniedException": apigatewayv2api.IdentityAccessDeniedException(),
		"BadRequestException":   apigatewayv2api.IdentityBadRequestException(),
		"ConflictException":     apigatewayv2api.IdentityConflictException(),
		"NotFoundException":     apigatewayv2api.IdentityNotFoundException(),
	} {
		if id.MessageKey != awserr.MessageLower {
			t.Errorf("%s: message key %v, want %v (wire key \"message\")", name, id.MessageKey, awserr.MessageLower)
		}
	}
}
