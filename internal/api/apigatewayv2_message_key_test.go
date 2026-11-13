package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/restjson"
)

// TestApiGatewayV2ErrorsWriteTheLowercaseMessageKey: AWS carries the text of
// an HTTP API control-plane error under "message", not "Message", for each of
// the four modeled shapes. The message texts are those of the recordings
// (testdata/parity/apigatewayv2/APIGW2-01/22, 08 and 01, APIGW2-15/06,
// aws-recorded); AWS pretty-prints two of the bodies, so bodies compare as
// decoded JSON.
func TestApiGatewayV2ErrorsWriteTheLowercaseMessageKey(t *testing.T) {
	for name, tc := range map[string]struct {
		id      awserr.Identity
		message string
		want    string
	}{
		"BadRequestException (APIGW2-01/22)": {
			apigatewayv2api.IdentityBadRequestException(),
			"Invalid integration URI specified",
			`{"message":"Invalid integration URI specified"}`,
		},
		"ConflictException (APIGW2-01/08)": {
			apigatewayv2api.IdentityConflictException(),
			"Stage already exists",
			`{"message":"Stage already exists"}`,
		},
		"NotFoundException (APIGW2-01/01)": {
			apigatewayv2api.IdentityNotFoundException(),
			"Invalid API identifier specified 123456789012:0000000000",
			`{"message":"Invalid API identifier specified 123456789012:0000000000"}`,
		},
		"AccessDeniedException (APIGW2-15/06)": {
			apigatewayv2api.IdentityAccessDeniedException(),
			"User: arn:aws:sts::123456789012:assumed-role/us1103-gw-caller/gw15 is not authorized to perform: apigateway:GET on resource: arn:aws:apigateway:us-east-2::/apis/hl9cpb8a0a/routes because no identity-based policy allows the apigateway:GET action",
			`{"message":"User: arn:aws:sts::123456789012:assumed-role/us1103-gw-caller/gw15 is not authorized to perform: apigateway:GET on resource: arn:aws:apigateway:us-east-2::/apis/hl9cpb8a0a/routes because no identity-based policy allows the apigateway:GET action"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost:4566/v2/apis/x", nil)
			e := &awserr.Error{ID: tc.id, Message: tc.message, Origin: awserr.Modeled{}}
			resp := restjson.Codec{}.EncodeError(r, nil, "7c7cb5ce-1a2b-4c3d-9e8f-0a1b2c3d4e5f", e)
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("body %s is not JSON: %v", raw, err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("body = %s, want %s", raw, tc.want)
			}
		})
	}
}
