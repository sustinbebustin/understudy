package ids_test

import (
	"fmt"
	"regexp"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/ids"
)

var (
	// apiID is an HTTP or REST API id: 10 lower-case alphanumerics, as in
	// AWS's samples r3pmxmplak, ktyvxmpls1, mnh1xmpli7 (apigateway-http.md
	// 2.5, aws-sample event-v2.json L29 and the services-apigateway page).
	apiID = regexp.MustCompile(`^[a-z0-9]{10}$`)
	// routeID is 7 characters, as 72jz1wk and go65gqi (apigateway-http.md
	// 2.5, the get-routes CLI example).
	routeID = regexp.MustCompile(`^[a-z0-9]{7}$`)
	// deploymentID is 6 characters, as 53lz9l, x1zwyv, dty748, shktxb
	// (apigateway-http.md 2.5, get-stages.rst L19 and create-deployment).
	deploymentID = regexp.MustCompile(`^[a-z0-9]{6}$`)
	// childID is any child id's alphabet. An integration id's length is
	// UNVERIFIED (apigateway-http.md 2.5, APIGW2-03), so only the alphabet
	// is asserted for it.
	childID = regexp.MustCompile(`^[a-z0-9]+$`)
	// apigwRequestID is the apigw-requestid header: 16 characters ending in
	// "=", with "-" in the alphabet, as JKJaXmPLvHcESHA=, LV7fzho-PHcEJPw=
	// and Mc7UVioPPHcEKPA= (apigateway-http.md 2.5).
	apigwRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{15}=$`)
)

// apigwDraws are the four API Gateway minting calls in one order.
func apigwDraws(src *ids.Source) []string {
	return []string{
		string(src.APIGatewayAPIID()),
		string(src.APIGatewayRouteID()),
		string(src.APIGatewayIntegrationID()),
		string(src.APIGatewayDeploymentID()),
	}
}

// A seeded source repeats its API Gateway ids, so a seeded run mints the
// same API, route, integration and deployment ids every time (ADR 0018).
func TestSeededAPIGatewayIDsRepeat(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Uint64().Draw(t, "seed")
		n := rapid.IntRange(1, 20).Draw(t, "rounds")
		a, b := ids.NewSeeded(seed), ids.NewSeeded(seed)
		for i := range n {
			x, y := apigwDraws(a), apigwDraws(b)
			if fmt.Sprint(x) != fmt.Sprint(y) {
				t.Fatalf("round %d with seed %d: %v and %v", i, seed, x, y)
			}
		}
	})
}

// Every minted id has the shape AWS's samples show (apigateway-http.md 2.5).
func TestAPIGatewayIDShapes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := ids.NewSeeded(rapid.Uint64().Draw(t, "seed"))
		if id := src.APIGatewayAPIID(); !apiID.MatchString(string(id)) {
			t.Fatalf("API id %q, want 10 of [a-z0-9]", id)
		}
		if id := src.APIGatewayRouteID(); !routeID.MatchString(string(id)) {
			t.Fatalf("route id %q, want 7 of [a-z0-9]", id)
		}
		if id := src.APIGatewayIntegrationID(); !childID.MatchString(string(id)) {
			t.Fatalf("integration id %q, want [a-z0-9]+", id)
		}
		if id := src.APIGatewayDeploymentID(); !deploymentID.MatchString(string(id)) {
			t.Fatalf("deployment id %q, want 6 of [a-z0-9]", id)
		}
	})
}

// A thousand API ids from one source are distinct, so one execute-api
// namespace never hands out an id twice in a run.
func TestAPIGatewayAPIIDsAreDistinct(t *testing.T) {
	src := ids.NewSeeded(1)
	seen := map[ids.APIGatewayAPIID]int{}
	for i := range 1000 {
		id := src.APIGatewayAPIID()
		if j, dup := seen[id]; dup {
			t.Fatalf("draw %d repeats draw %d: %q", i, j, id)
		}
		seen[id] = i
	}
}

// API ids draw from their own stream and child ids from theirs: minting a
// route, integration or deployment id leaves the API id stream where it
// was, and the reverse, so REST and HTTP APIs share one API id stream
// (ADR 0018).
func TestAPIGatewayStreamsAreSeparate(t *testing.T) {
	src := ids.NewSeeded(7)
	src.APIGatewayRouteID()
	src.APIGatewayIntegrationID()
	src.APIGatewayDeploymentID()
	pos, err := src.Positions()
	if err != nil {
		t.Fatal(err)
	}
	if pos[ids.KindAPIGatewayChildID] == 0 || pos[ids.KindAPIGatewayAPIID] != 0 {
		t.Errorf("after three child ids: positions api %d, child %d; want api 0, child > 0", pos[ids.KindAPIGatewayAPIID], pos[ids.KindAPIGatewayChildID])
	}
	src.APIGatewayAPIID()
	again, err := src.Positions()
	if err != nil {
		t.Fatal(err)
	}
	if again[ids.KindAPIGatewayAPIID] == 0 || again[ids.KindAPIGatewayChildID] != pos[ids.KindAPIGatewayChildID] {
		t.Errorf("after an API id: positions api %d, child %d; want api > 0, child %d", again[ids.KindAPIGatewayAPIID], again[ids.KindAPIGatewayChildID], pos[ids.KindAPIGatewayChildID])
	}
}

// The apigw-requestid value derived from a call id has AWS's shape, is the
// same for the same call id and differs between call ids, so the header
// and the event's requestContext.requestId can both be derived from the
// call (switch apigatewayv2.payload.unrecorded, RequestIDIsHeader).
func TestAPIGatewayRequestIDFor(t *testing.T) {
	src := ids.NewSeeded(3)
	seen := map[ids.APIGatewayRequestID]ids.RequestID{}
	for range 1000 {
		call := src.RequestID()
		got := ids.APIGatewayRequestIDFor(call)
		if !apigwRequestID.MatchString(string(got)) {
			t.Fatalf("APIGatewayRequestIDFor(%s) = %q, want 16 URL-safe base64 characters ending in =", call, got)
		}
		if again := ids.APIGatewayRequestIDFor(call); again != got {
			t.Fatalf("APIGatewayRequestIDFor(%s) = %q, then %q", call, got, again)
		}
		if prev, dup := seen[got]; dup {
			t.Fatalf("call ids %s and %s both give %q", prev, call, got)
		}
		seen[got] = call
	}
	pos, err := src.Positions()
	if err != nil {
		t.Fatal(err)
	}
	if pos[ids.KindAPIGatewayAPIID] != 0 || pos[ids.KindAPIGatewayChildID] != 0 {
		t.Errorf("APIGatewayRequestIDFor drew from an API Gateway stream: %v", pos)
	}
}
