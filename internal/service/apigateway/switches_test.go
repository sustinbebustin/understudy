package apigateway_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// The five switches of the unit (spec 6b "Fidelity switches"), in its
// order, with a Ref into apigateway-rest.md, the recording cases that
// settle it and no parity scenario (the replayer does not route
// execute-api requests); api-status and proxy.unrecorded are AWS's as
// APIGW1-03, -10 and -16 recorded them, the others keep an unrecorded
// part; Switches lists each constructor's value.
func TestSwitches(t *testing.T) {
	invoke := []string{"apigateway.InvokeRestApi"}
	want := []struct {
		name, value string
		label       evidence.Label
		records     []string
		ops         []string
	}{
		{"apigateway.restapi.api-status", fmt.Sprint(apigateway.APIStatusSwitch().Value), evidence.AWSRecorded, []string{"APIGW1-03"}, []string{"apigateway.CreateRestApi", "apigateway.UpdateRestApi", "apigateway.GetRestApi", "apigateway.GetRestApis"}},
		{"apigateway.invoke.routing", fmt.Sprint(apigateway.RoutingSwitch().Value), evidence.Inferred, []string{"APIGW1-07", "APIGW1-08"}, invoke},
		{"apigateway.lambda.permission", fmt.Sprint(apigateway.PermissionSwitch().Value), evidence.Inferred, []string{"APIGW1-15"}, invoke},
		{"apigateway.mock.selection", fmt.Sprint(apigateway.MockSelectionSwitch().Value), evidence.Inferred, []string{"APIGW1-09", "APIGW1-11", "APIGW1-12"}, invoke},
		{"apigateway.proxy.unrecorded", fmt.Sprint(apigateway.ProxyUnrecordedSwitch().Value), evidence.AWSRecorded, []string{"APIGW1-10", "APIGW1-16"}, invoke},
	}
	run(t, func(h *harness) {
		got := h.svc.Switches()
		if len(got) != len(want) {
			t.Fatalf("Switches() = %d entries %+v, want %d", len(got), got, len(want))
		}
		for i, w := range want {
			e := got[i]
			if e.Name != w.name {
				t.Errorf("switch %d = %s, want %s", i, e.Name, w.name)
				continue
			}
			if e.Value != w.value || e.Label != w.label || !slices.Equal(e.Ops, w.ops) {
				t.Errorf("%s = value %q, label %s, ops %v\nwant value %q, label %s, ops %v", e.Name, e.Value, e.Label, e.Ops, w.value, w.label, w.ops)
			}
			if !strings.HasPrefix(e.Ref, "docs/research/apigateway-rest.md#") {
				t.Errorf("%s Ref = %q, want an anchor in docs/research/apigateway-rest.md", e.Name, e.Ref)
			}
			for _, r := range w.records {
				if !strings.Contains(e.Record, r) {
					t.Errorf("%s Record = %q, want it to name %s", e.Name, e.Record, r)
				}
			}
			if _, ok := e.Settles.(fidelity.NoScenario); !ok {
				t.Errorf("%s Settles = %#v, want NoScenario", e.Name, e.Settles)
			}
		}
	})
}

// The documented and recorded parts of the switch values stay AWS's: the
// recorded Lambda failure body "Internal server error" for the permission
// denial (apigateway-rest.md 5.3, 4.7) and the gateway response statuses
// 415 UNSUPPORTED_MEDIA_TYPE and 504 INTEGRATION_TIMEOUT (aws-doc
// supported-gateway-response-types, apigateway-rest.md 4.7).
func TestSwitchValuesKeepTheDocumentedParts(t *testing.T) {
	// APIGW1-15 and APIGW1-10, recorded 2026-11-03.
	for name, c := range map[string]struct{ got, want apigateway.GatewayAnswer }{
		"permission Denial": {apigateway.PermissionSwitch().Value.Denial, apigateway.GatewayAnswer{Status: 500, Body: `{"message": "Internal server error"}`, ErrorType: "InternalServerErrorException"}},
		"Timeout":           {apigateway.ProxyUnrecordedSwitch().Value.Timeout, apigateway.GatewayAnswer{Status: 504, Body: `{"message": "Endpoint request timed out"}`, ErrorType: "InternalServerErrorException"}},
	} {
		if c.got != c.want {
			t.Errorf("%s = %+v, want %+v", name, c.got, c.want)
		}
	}
	if s := apigateway.MockSelectionSwitch().Value.UnsupportedMediaType.Status; s != http.StatusUnsupportedMediaType {
		t.Errorf("UnsupportedMediaType status = %d, want 415", s)
	}
	if s := apigateway.ProxyUnrecordedSwitch().Value.Timeout.Status; s != http.StatusGatewayTimeout {
		t.Errorf("Timeout status = %d, want 504", s)
	}
	if s := apigateway.APIStatusSwitch().Value; !s.AfterCreate.Known() || !s.AfterUpdate.Known() {
		t.Errorf("APIStatus = %+v, want values of the ApiStatus enum ([S] com.amazonaws.apigateway#ApiStatus)", s)
	}
}

// The fixed answers API Gateway writes itself (contract 7.4):
// MISSING_AUTHENTICATION_TOKEN 403 with its body and error type
// (aws-recorded, test_apigateway_common.snapshot.json:L1113-L1132,
// apigateway-rest.md finding 3), the Lambda proxy failure 502 (aws-recorded,
// test_apigateway_lambda.snapshot.json:L1669-L1797, 5.2, 5.3), the
// configuration error 500 (aws-recorded,
// test_apigateway_integrations.snapshot.json:L554-L582, 4.7) and
// REQUEST_TOO_LARGE 413 "HTTP content length exceeded 10485760 bytes"
// (aws-doc supported-gateway-response-types, 2.6). The API-key 403 and the
// oversized-event answer are inferred: status only.
func TestFixedAnswers(t *testing.T) {
	for name, c := range map[string]struct{ got, want apigateway.GatewayAnswer }{
		"MissingAuthenticationToken": {apigateway.MissingAuthenticationToken(), apigateway.GatewayAnswer{Status: 403, Body: `{"message":"Missing Authentication Token"}`, ErrorType: "MissingAuthenticationTokenException"}},
		"ProxyFailure":               {apigateway.ProxyFailure(), apigateway.GatewayAnswer{Status: 502, Body: `{"message": "Internal server error"}`}},
		"ConfigurationError":         {apigateway.ConfigurationError(), apigateway.GatewayAnswer{Status: 500, Body: `{"message":"Internal server error"}`, ErrorType: "InternalServerErrorException"}},
	} {
		if c.got != c.want {
			t.Errorf("%s = %+v, want %+v", name, c.got, c.want)
		}
	}
	if a := apigateway.RequestTooLarge(); a.Status != http.StatusRequestEntityTooLarge || !strings.Contains(a.Body, "HTTP content length exceeded 10485760 bytes") {
		t.Errorf("RequestTooLarge = %+v, want 413 with the documented text", a)
	}
	if apigateway.MaxBody != 10_485_760 {
		t.Errorf("MaxBody = %d, want 10485760 (apigateway-rest.md 2.6)", apigateway.MaxBody)
	}
	if a := apigateway.APIKeyRequired(); a.Status != http.StatusForbidden {
		t.Errorf("APIKeyRequired status = %d, want 403 (spec 6b enforce matrix)", a.Status)
	}
	if a := apigateway.EventTooLarge(); a.Status < 500 {
		t.Errorf("EventTooLarge status = %d, want a 5xx", a.Status)
	}
}

// The source ARN is the execute-api ARN of the request:
// arn:aws:execute-api:{region}:{account-id}:{api-id}/{stage}/{http-method}/{resource-path}
// (aws-doc arn-format-reference, apigateway-rest.md 3.10, 5.4). How ANY
// and the root resource are written is switch apigateway.lambda.permission
// (APIGW1-15): the test reads the rule from the switch's description.
func TestSourceARN(t *testing.T) {
	if got, want := apigateway.SourceARN("us-east-1", "000000000000", "a1b2c3d4e5", "s", "GET", "/items/{id}"), "arn:aws:execute-api:us-east-1:000000000000:a1b2c3d4e5/s/GET/items/{id}"; got != want {
		t.Errorf("SourceARN(GET /items/{id}) = %q, want %q", got, want)
	}
	rule := apigateway.PermissionSwitch().Value.SourceARN
	if strings.Contains(rule, "ANY as written") {
		if got, want := apigateway.SourceARN("us-east-1", "000000000000", "a1b2c3d4e5", "s", "ANY", "/{proxy+}"), "arn:aws:execute-api:us-east-1:000000000000:a1b2c3d4e5/s/ANY/{proxy+}"; got != want {
			t.Errorf("SourceARN(ANY /{proxy+}) = %q, want %q (switch: %s)", got, want, rule)
		}
	} else {
		t.Errorf("switch apigateway.lambda.permission SourceARN = %q names no rule for ANY", rule)
	}
	if strings.Contains(rule, "empty last segment for the root resource") {
		if got, want := apigateway.SourceARN("us-east-1", "000000000000", "a1b2c3d4e5", "s", "GET", "/"), "arn:aws:execute-api:us-east-1:000000000000:a1b2c3d4e5/s/GET/"; got != want {
			t.Errorf("SourceARN(GET /) = %q, want %q (switch: %s)", got, want, rule)
		}
	} else {
		t.Errorf("switch apigateway.lambda.permission SourceARN = %q names no rule for the root resource", rule)
	}
}
