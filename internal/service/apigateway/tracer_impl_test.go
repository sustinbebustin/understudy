package apigateway_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// The root resource's ANY method serves a verb the root has no explicit
// method for (apigateway-rest.md 4.4); HEAD is not served by GET (switch
// apigateway.invoke.routing, HeadMatchesGet false); an AWS_IAM method
// answers 501 at invoke, without invoking.
func TestTracerRootMethods(t *testing.T) {
	routing := apigateway.RoutingSwitch().Value
	run(t, func(h *harness) {
		id, _ := h.proxyAPI()
		root := h.rootOf(id)
		if rec := h.send(h.request(http.MethodHead, pathURL(id, "/s/"), "")); !routing.Methods.HeadMatchesGet {
			wantAnswer(t, rec, apigateway.MissingAuthenticationToken(), "HEAD on a GET-only root")
		}
		h.notInvoked("HEAD on a GET-only root")
		h.proxy(id, root, "ANY", h.fnARN)
		h.deploy(id)
		if rec := h.send(h.request(http.MethodPost, pathURL(id, "/s"), "x")); rec.Code != http.StatusOK {
			t.Errorf("POST /s on the root's ANY = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		if ev := h.only().event(h); ev["httpMethod"] != http.MethodPost || ev["resource"] != "/" {
			t.Errorf("event httpMethod %v, resource %v; want POST, /", ev["httpMethod"], ev["resource"])
		}
		must(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "DELETE", AuthorizationType: "AWS_IAM"})
		must(h, apigatewayapi.OpPutIntegration, proxyInput(id, root, "DELETE", h.fnARN))
		h.deploy(id)
		want501(t, h.send(h.request(http.MethodDelete, pathURL(id, "/s/"), "")), "AWS_IAM", "an AWS_IAM method")
		h.wantJournaled501("an AWS_IAM method")
		if n := len(h.fn.invocations()); n != 1 {
			t.Errorf("the function ran %d times, want once", n)
		}
	})
}

// A stage serves the deployment it points at: a method added after it is
// not served until CreateDeployment repoints the stage (apigateway-rest.md
// 12.5, finding 12).
func TestTracerStageServesItsSnapshot(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI()
		h.proxy(id, h.rootOf(id), "POST", h.fnARN)
		wantAnswer(t, h.send(h.request(http.MethodPost, pathURL(id, "/s/"), "x")), apigateway.MissingAuthenticationToken(), "POST before redeploying")
		h.deploy(id)
		if rec := h.send(h.request(http.MethodPost, pathURL(id, "/s/"), "x")); rec.Code != http.StatusOK {
			t.Errorf("POST after redeploying = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}
