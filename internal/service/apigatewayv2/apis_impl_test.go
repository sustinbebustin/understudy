package apigatewayv2_test

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// tryCreate runs CreateApi and answers its error, nil when it succeeded.
func (h *harness) tryCreate(in *apigatewayv2api.CreateApiInput) *awserr.Error {
	h.Helper()
	_, err := h.Invoke(h.Context(), apigatewayv2api.OpCreateApi, in)
	return err
}

// isBadRequest reports a BadRequestException 400 with a message.
func isBadRequest(err *awserr.Error) bool {
	return err != nil && err.ID.Shape == badRequest.Shape && err.ID.Status == badRequest.Status && err.Message != ""
}

// Each member limit of 13.5 holds at its bounds and only there: name 1..128,
// description 0..1024 and version 1..64 characters on CreateApi and
// UpdateApi (apigateway-http.md 13.5). A refused UpdateApi changes nothing.
func TestAPIMemberLengthLimits(t *testing.T) {
	limits := []struct {
		member         string
		minLen, maxLen int
		create         func(v string) *apigatewayv2api.CreateApiInput
		update         func(id, v string) *apigatewayv2api.UpdateApiInput
		read           func(*apigatewayv2api.GetApiOutput) string
	}{
		{
			"name", 1, 128,
			func(v string) *apigatewayv2api.CreateApiInput {
				return &apigatewayv2api.CreateApiInput{Name: v, ProtocolType: apigatewayv2api.ProtocolTypeHttp}
			},
			func(id, v string) *apigatewayv2api.UpdateApiInput {
				return &apigatewayv2api.UpdateApiInput{ApiId: id, Name: new(v)}
			},
			func(o *apigatewayv2api.GetApiOutput) string { return *o.Name },
		},
		{
			"description", 0, 1024,
			func(v string) *apigatewayv2api.CreateApiInput {
				return &apigatewayv2api.CreateApiInput{Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp, Description: new(v)}
			},
			func(id, v string) *apigatewayv2api.UpdateApiInput {
				return &apigatewayv2api.UpdateApiInput{ApiId: id, Description: new(v)}
			},
			func(o *apigatewayv2api.GetApiOutput) string { return deref(o.Description) },
		},
		{
			"version", 1, 64,
			func(v string) *apigatewayv2api.CreateApiInput {
				return &apigatewayv2api.CreateApiInput{Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp, Version: new(v)}
			},
			func(id, v string) *apigatewayv2api.UpdateApiInput {
				return &apigatewayv2api.UpdateApiInput{ApiId: id, Version: new(v)}
			},
			func(o *apigatewayv2api.GetApiOutput) string { return deref(o.Version) },
		},
	}
	for _, l := range limits {
		t.Run(l.member, func(t *testing.T) {
			run(t, func(h *harness) {
				id := h.apiID("target")
				rapid.Check(t, func(rt *rapid.T) {
					n := rapid.IntRange(0, l.maxLen+8).Draw(rt, "length")
					v := strings.Repeat("v", n)
					within := l.minLen <= n && n <= l.maxLen
					if err := h.tryCreate(l.create(v)); within != (err == nil) || !within && !isBadRequest(err) {
						rt.Fatalf("CreateApi with a %d-character %s = %v; want accepted only within %d..%d, else BadRequestException", n, l.member, err, l.minLen, l.maxLen)
					}
					before := l.read(must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}))
					_, err := h.Invoke(h.Context(), apigatewayv2api.OpUpdateApi, l.update(id, v))
					if within != (err == nil) || !within && !isBadRequest(err) {
						rt.Fatalf("UpdateApi with a %d-character %s = %v; want accepted only within %d..%d, else BadRequestException", n, l.member, err, l.minLen, l.maxLen)
					}
					after := l.read(must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}))
					if within && after != v || !within && after != before {
						rt.Fatalf("%s after UpdateApi = %q, want %q", l.member, after, map[bool]string{true: v, false: before}[within])
					}
				})
			})
		})
	}
}

// CORS MaxAge holds -1..86400 and nothing else, the model's @range
// (apigateway-http.md 2.4).
func TestCORSMaxAgeProperty(t *testing.T) {
	run(t, func(h *harness) {
		rapid.Check(t, func(rt *rapid.T) {
			age := rapid.Int32Range(-1000, 100_000).Draw(rt, "maxAge")
			err := h.tryCreate(&apigatewayv2api.CreateApiInput{Name: "c", ProtocolType: apigatewayv2api.ProtocolTypeHttp, CorsConfiguration: &apigatewayv2api.Cors{MaxAge: new(age)}})
			if within := -1 <= age && age <= 86400; within != (err == nil) || !within && !isBadRequest(err) {
				rt.Fatalf("MaxAge %d = %v, want accepted only within -1..86400, else BadRequestException", age, err)
			}
		})
	})
}

// tagsOf makes n tags whose keys are k<i> padded to keyLen characters (at
// least the k<i> prefix) with values of valueLen characters.
func tagsOf(n, keyLen, valueLen int) map[string]string {
	tags := make(map[string]string, n)
	for i := range n {
		k := "k" + strconv.Itoa(i)
		if len(k) < keyLen {
			k += strings.Repeat("x", keyLen-len(k))
		}
		tags[k] = strings.Repeat("v", valueLen)
	}
	return tags
}

// Tags hold at most 50 per resource ("Tags per stage: 50", apigateway-http.md
// 3.6), keys of 1..128 and values of 0..256 characters (inferred,
// APIGW2-01 cases 24 and 25), on CreateApi and on TagResource, where the
// count is the merged one; a refused TagResource changes nothing.
func TestTagLimits(t *testing.T) {
	run(t, func(h *harness) {
		rapid.Check(t, func(rt *rapid.T) {
			n := rapid.IntRange(0, 60).Draw(rt, "count")
			err := h.tryCreate(&apigatewayv2api.CreateApiInput{Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp, Tags: tagsOf(n, 0, 1)})
			if within := n <= 50; within != (err == nil) || !within && !isBadRequest(err) {
				rt.Fatalf("CreateApi with %d tags = %v, want accepted only up to 50, else BadRequestException", n, err)
			}
		})
		rapid.Check(t, func(rt *rapid.T) {
			keyLen := rapid.IntRange(0, 140).Draw(rt, "key length")
			tags := map[string]string{strings.Repeat("k", keyLen): "v"}
			err := h.tryCreate(&apigatewayv2api.CreateApiInput{Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp, Tags: tags})
			if within := 1 <= keyLen && keyLen <= 128; within != (err == nil) || !within && !isBadRequest(err) {
				rt.Fatalf("a %d-character tag key = %v, want accepted only within 1..128, else BadRequestException", keyLen, err)
			}
		})
		rapid.Check(t, func(rt *rapid.T) {
			valueLen := rapid.IntRange(0, 270).Draw(rt, "value length")
			err := h.tryCreate(&apigatewayv2api.CreateApiInput{Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp, Tags: tagsOf(1, 0, valueLen)})
			if within := valueLen <= 256; within != (err == nil) || !within && !isBadRequest(err) {
				rt.Fatalf("a %d-character tag value = %v, want accepted only up to 256, else BadRequestException", valueLen, err)
			}
		})
		rapid.Check(t, func(rt *rapid.T) {
			held := rapid.IntRange(0, 50).Draw(rt, "held")
			added := rapid.IntRange(0, 20).Draw(rt, "added")
			id := deref(must(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp, Tags: tagsOf(held, 0, 1)}).ApiId)
			more := make(map[string]string, added)
			for i := range added {
				more["new"+strconv.Itoa(i)] = "v"
			}
			_, err := h.Invoke(h.Context(), apigatewayv2api.OpTagResource, &apigatewayv2api.TagResourceInput{ResourceArn: apiARN(id), Tags: more})
			if within := held+added <= 50; within != (err == nil) || !within && !isBadRequest(err) {
				rt.Fatalf("TagResource of %d tags onto %d = %v, want accepted only up to 50 in all, else BadRequestException", added, held, err)
			}
			got := must(h, apigatewayv2api.OpGetTags, &apigatewayv2api.GetTagsInput{ResourceArn: apiARN(id)}).Tags
			if want := held + added; err != nil {
				want = held
				if len(got) != want {
					rt.Fatalf("after a refused TagResource the API holds %d tags, want its %d", len(got), want)
				}
			} else if len(got) != want {
				rt.Fatalf("after TagResource the API holds %d tags, want %d", len(got), want)
			}
		})
	})
}

// Paging through GetApis with any page size lists every API once, in
// creation order (inferred, APIGW2-03), with no page larger than asked;
// maxResults that is not a positive integer, and a token no page answered,
// are BadRequestException (apigateway-http.md 2.2).
func TestGetApisPagingProperty(t *testing.T) {
	run(t, func(h *harness) {
		rapid.Check(t, func(rt *rapid.T) {
			made := rapid.IntRange(0, 12).Draw(rt, "apis")
			size := rapid.IntRange(1, 6).Draw(rt, "page size")
			ids := make([]string, 0, made)
			for i := range made {
				ids = append(ids, h.apiID("p"+strconv.Itoa(i)))
			}
			all := idsOf(h.listAPIs(""))
			if !slices.Equal(all[len(all)-made:], ids) {
				rt.Fatalf("GetApis ends with %v, want the APIs just made in creation order, %v", all[len(all)-made:], ids)
			}
			if paged := idsOf(h.listAPIs(strconv.Itoa(size))); !slices.Equal(paged, all) {
				rt.Fatalf("GetApis in pages of %d = %v, want %v", size, paged, all)
			}
			for _, id := range ids {
				must(h, apigatewayv2api.OpDeleteApi, &apigatewayv2api.DeleteApiInput{ApiId: id})
			}
		})
		rapid.Check(t, func(rt *rapid.T) {
			bad := rapid.OneOf(rapid.Map(rapid.IntRange(-5, 0), strconv.Itoa), rapid.StringMatching(`[a-z ]{0,4}`), rapid.Just("1.5")).Draw(rt, "maxResults")
			if err := fail(h, apigatewayv2api.OpGetApis, &apigatewayv2api.GetApisInput{MaxResults: new(bad)}); !isBadRequest(err) {
				rt.Fatalf("maxResults %q = %v, want BadRequestException", bad, err)
			}
		})
		wantError(t, fail(h, apigatewayv2api.OpGetApis, &apigatewayv2api.GetApisInput{NextToken: new("not a token!")}), badRequest)
	})
}

func idsOf(apis []apigatewayv2api.Api) []string {
	out := make([]string, 0, len(apis))
	for i := range apis {
		out = append(out, deref(apis[i].ApiId))
	}
	return out
}

// A page's nextToken still continues the listing after the API it would
// have started with is deleted: nothing after it is skipped.
func TestGetApisTokenSurvivesADelete(t *testing.T) {
	run(t, func(h *harness) {
		ids := []string{h.apiID("a"), h.apiID("b"), h.apiID("c")}
		first := must(h, apigatewayv2api.OpGetApis, &apigatewayv2api.GetApisInput{MaxResults: new("1")})
		if got := idsOf(first.Items); !slices.Equal(got, ids[:1]) || first.NextToken == nil {
			t.Fatalf("first page = %v with token %v, want [%s] and a token", got, first.NextToken, ids[0])
		}
		must(h, apigatewayv2api.OpDeleteApi, &apigatewayv2api.DeleteApiInput{ApiId: ids[1]})
		rest := must(h, apigatewayv2api.OpGetApis, &apigatewayv2api.GetApisInput{NextToken: first.NextToken})
		if got := idsOf(rest.Items); !slices.Equal(got, ids[2:]) || rest.NextToken != nil {
			t.Errorf("after deleting %s the next page = %v with token %v, want [%s] and no token", ids[1], got, rest.NextToken, ids[2])
		}
	})
}

// ApiEndpoint follows the endpoint the client addressed: its scheme, and
// its port only when it has one (apigateway-http.md 13.1.4).
func TestApiEndpointFollowsTheAddressedEndpoint(t *testing.T) {
	for _, c := range []struct {
		endpoint kernel.Endpoint
		want     string
	}{
		{kernel.Endpoint{Scheme: "https", Host: "localhost:4567"}, "https://%s.execute-api.us-east-1.localhost:4567"},
		{kernel.Endpoint{Scheme: "http", Host: "localhost"}, "http://%s.execute-api.us-east-1.localhost"},
	} {
		run(t, func(h *harness) {
			h.Endpoint = c.endpoint
			id := h.apiID("t")
			want := strings.Replace(c.want, "%s", id, 1)
			if got := deref(must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}).ApiEndpoint); got != want {
				t.Errorf("ApiEndpoint on %s = %q, want %q", c.endpoint, got, want)
			}
		})
	}
}

// UpdateApi stores and returns every HTTP API member it takes, the braced
// route selection expression as sent (apigateway-http.md 12 item 5), and
// keeps what it does not carry; the API's settings survive Export and
// Import (ADR 0009).
func TestUpdateApiMembersRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		id := deref(must(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{
			Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp, Version: new("1"), Tags: map[string]string{"k": "v"},
			CorsConfiguration: &apigatewayv2api.Cors{AllowOrigins: []string{"https://a.example"}, AllowHeaders: []string{}, AllowCredentials: new(false), MaxAge: new(int32(0))},
		}).ApiId)
		dual := apigatewayv2api.IpAddressTypeDualstack
		out := must(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{
			ApiId: id, RouteSelectionExpression: new("${request.method} ${request.path}"), ApiKeySelectionExpression: new("$context.authorizer.usageIdentifierKey"),
			DisableSchemaValidation: new(true), IpAddressType: &dual, Description: new(""),
		})
		if deref(out.RouteSelectionExpression) != "${request.method} ${request.path}" || deref(out.ApiKeySelectionExpression) != "$context.authorizer.usageIdentifierKey" ||
			!deref(out.DisableSchemaValidation) || deref(out.IpAddressType) != dual || out.Description == nil || deref(out.Description) != "" {
			t.Errorf("UpdateApi = %+v, want each member it carried", out)
		}
		if deref(out.Name) != "t" || deref(out.Version) != "1" || !maps.Equal(out.Tags, map[string]string{"k": "v"}) || out.CorsConfiguration == nil {
			t.Errorf("UpdateApi = %+v, want name, version, tags and CORS kept", out)
		}
		read := func() string {
			b, err := json.Marshal(must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}))
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		before := read()
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		if after := read(); after != before {
			t.Errorf("after Export and Import:\n%s\nwant\n%s", after, before)
		}
		cors := must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}).CorsConfiguration
		if cors == nil || cors.AllowHeaders == nil || len(cors.AllowHeaders) != 0 || cors.AllowMethods != nil {
			t.Errorf("CorsConfiguration after Import = %+v, want the empty AllowHeaders kept and the unsent AllowMethods absent", cors)
		}
	})
}

// DeleteCorsConfiguration of an API without one succeeds (inferred); of no
// API it is NotFoundException (apigateway-http.md 2.2).
func TestDeleteCorsConfigurationWithoutOne(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		must(h, apigatewayv2api.OpDeleteCorsConfiguration, &apigatewayv2api.DeleteCorsConfigurationInput{ApiId: id})
		wantError(t, fail(h, apigatewayv2api.OpDeleteCorsConfiguration, &apigatewayv2api.DeleteCorsConfigurationInput{ApiId: "zzzzzzzzzz"}), notFound)
	})
}

// A deleted API is gone from the data plane too: its id answers the
// front.answers switch's UnknownAPI.
func TestDeletedAPIIsUnknownToTheDataPlane(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		must(h, apigatewayv2api.OpDeleteApi, &apigatewayv2api.DeleteApiInput{ApiId: id})
		wantAnswer(t, h.get(id, "/hello"), apigatewayv2.FrontAnswersSwitch().Value.UnknownAPI, "a deleted API")
		h.notInvoked("a deleted API")
	})
}

// A request with an Origin header to an API with a CORS configuration
// answers 501, since understudy does not apply CORS; the same request
// without Origin reaches the function, and so does one with Origin once
// DeleteCorsConfiguration removes the configuration.
func TestCORSRequestAnswers501(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		must(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: id, CorsConfiguration: &apigatewayv2api.Cors{AllowOrigins: []string{"*"}}})
		withOrigin := func() *http.Request {
			r := h.request(http.MethodGet, pathURL(id, "/hello"), "")
			r.Header.Set("Origin", "https://app.example")
			return r
		}
		want501(t, h.send(withOrigin()), "a CORS request")
		h.notInvoked("a CORS request")
		if rec := h.get(id, "/hello"); rec.Code != http.StatusOK {
			t.Errorf("GET without Origin = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		must(h, apigatewayv2api.OpDeleteCorsConfiguration, &apigatewayv2api.DeleteCorsConfigurationInput{ApiId: id})
		if rec := h.send(withOrigin()); rec.Code != http.StatusOK {
			t.Errorf("GET with Origin after DeleteCorsConfiguration = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}

// Tags given at CreateStage are the stage's: GetTags reads them from the
// stage ARN, UntagResource removes them there, and the API's own tags are
// apart. An ARN of another region, of a missing stage, or of a resource
// that takes no tags is NotFoundException; an ARN of another service is a
// bad request (apigateway-http.md 2.5, 3.6).
func TestStageTagsAndResourceArns(t *testing.T) {
	run(t, func(h *harness) {
		id := deref(must(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp, Tags: map[string]string{"api": "1"}}).ApiId)
		created := must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod", Tags: map[string]string{"a": "1", "b": "2"}})
		if !maps.Equal(created.Tags, map[string]string{"a": "1", "b": "2"}) {
			t.Errorf("CreateStage Tags = %v, want a=1 b=2", created.Tags)
		}
		stage := apiARN(id) + "/stages/prod"
		must(h, apigatewayv2api.OpUntagResource, &apigatewayv2api.UntagResourceInput{ResourceArn: stage, TagKeys: []string{"a", "absent"}})
		if got := must(h, apigatewayv2api.OpGetTags, &apigatewayv2api.GetTagsInput{ResourceArn: stage}).Tags; !maps.Equal(got, map[string]string{"b": "2"}) {
			t.Errorf("stage GetTags = %v, want b=2", got)
		}
		if got := must(h, apigatewayv2api.OpGetTags, &apigatewayv2api.GetTagsInput{ResourceArn: apiARN(id)}).Tags; !maps.Equal(got, map[string]string{"api": "1"}) {
			t.Errorf("API GetTags = %v, want api=1", got)
		}
		for _, arn := range []string{
			"arn:aws:apigateway:us-west-2::/apis/" + id,
			"arn:aws:apigateway:" + region + ":" + account + ":/apis/" + id,
			apiARN(id) + "/stages/nope",
			apiARN(id) + "/routes/abc1234",
			"arn:aws:apigateway:" + region + "::/restapis/" + id,
		} {
			wantError(t, fail(h, apigatewayv2api.OpGetTags, &apigatewayv2api.GetTagsInput{ResourceArn: arn}), notFound)
			wantError(t, fail(h, apigatewayv2api.OpTagResource, &apigatewayv2api.TagResourceInput{ResourceArn: arn, Tags: map[string]string{"x": "y"}}), notFound)
		}
		wantError(t, fail(h, apigatewayv2api.OpGetTags, &apigatewayv2api.GetTagsInput{ResourceArn: "arn:aws:sqs:us-east-1:000000000000:q"}), badRequest)
	})
}

// wire sends a request signed for apigateway, the signing name HTTP APIs
// share with REST APIs (ADR 0019), through the edge and answers the status
// and body.
func (h *harness) wire(method, target, body string) (status int, answer string) {
	h.Helper()
	r := httptest.NewRequestWithContext(h.Context(), method, "http://localhost:4566"+target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	reg, err := aws.KnownRegions().Parse(region)
	if err != nil {
		h.Fatal(err)
	}
	auth.Sign(r, []byte(body), auth.Signer{AccessKeyID: account, SecretAccessKey: "secret", Region: reg, Service: "apigateway"}, h.Clock.Wall())
	rec := httptest.NewRecorder()
	h.edge.ServeHTTP(rec, r)
	got, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		h.Fatal(err)
	}
	return rec.Code, string(got)
}

// The tag operations take the resource ARN as a path label, so clients
// send its "/" as %2F (apigateway-http.md 2.2); dispatch on the escaped
// path keeps the ARN whole, and TagResource answers 201, GetTags the tags
// and UntagResource 204 with the tagKeys query list.
func TestTagsOnTheWire(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		label := url.PathEscape(apiARN(id))
		if !strings.Contains(label, "%2F") {
			t.Fatalf("label %q keeps the ARN's slashes", label)
		}
		if code, body := h.wire(http.MethodPost, "/v2/tags/"+label, `{"tags":{"env":"dev","team":"a"}}`); code != http.StatusCreated {
			t.Fatalf("TagResource = %d %s, want 201", code, body)
		}
		if code, body := h.wire(http.MethodDelete, "/v2/tags/"+label+"?tagKeys=team", ""); code != http.StatusNoContent {
			t.Fatalf("UntagResource = %d %s, want 204", code, body)
		}
		code, body := h.wire(http.MethodGet, "/v2/tags/"+label, "")
		var got struct {
			Tags map[string]string `json:"tags"`
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil || code != http.StatusOK || !maps.Equal(got.Tags, map[string]string{"env": "dev"}) {
			t.Errorf("GetTags = %d %s, want 200 with tags env=dev", code, body)
		}
	})
}

// Step 8 asks about each Api and tag operation as apigateway:<its HTTP
// method> on the resource the 8.1 table gives it: /apis for CreateApi and
// GetApis, /apis/<api-id> for the API's own calls, /apis/<api-id>/cors for
// DeleteCorsConfiguration, and /tags/<the ARN with every byte outside
// [A-Za-z0-9-_.~] percent-encoded> for the tag calls (apigateway-http.md
// 8.1; the tags form UNVERIFIED for HTTP APIs, APIGW2-15).
func TestStep8AsksAboutTheApiAndTagPairs(t *testing.T) {
	g := newGate()
	run(t, func(h *harness) {
		id := h.apiID("t")
		api := apiARN(id)
		must(h, apigatewayv2api.OpGetApis, &apigatewayv2api.GetApisInput{})
		must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id})
		must(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: id, Description: new("d")})
		must(h, apigatewayv2api.OpDeleteCorsConfiguration, &apigatewayv2api.DeleteCorsConfigurationInput{ApiId: id})
		must(h, apigatewayv2api.OpTagResource, &apigatewayv2api.TagResourceInput{ResourceArn: api, Tags: map[string]string{"a": "b"}})
		must(h, apigatewayv2api.OpGetTags, &apigatewayv2api.GetTagsInput{ResourceArn: api})
		must(h, apigatewayv2api.OpUntagResource, &apigatewayv2api.UntagResourceInput{ResourceArn: api, TagKeys: []string{"a"}})
		must(h, apigatewayv2api.OpDeleteApi, &apigatewayv2api.DeleteApiInput{ApiId: id})

		prefix := "arn:aws:apigateway:" + region + "::"
		tags := prefix + "/tags/arn%3Aaws%3Aapigateway%3A" + region + "%3A%3A%2Fapis%2F" + id
		for op, w := range map[string][2]string{
			"CreateApi": {"apigateway:POST", prefix + "/apis"}, "GetApis": {"apigateway:GET", prefix + "/apis"},
			"GetApi": {"apigateway:GET", api}, "UpdateApi": {"apigateway:PATCH", api}, "DeleteApi": {"apigateway:DELETE", api},
			"DeleteCorsConfiguration": {"apigateway:DELETE", api + "/cors"},
			"TagResource":             {"apigateway:POST", tags}, "GetTags": {"apigateway:GET", tags}, "UntagResource": {"apigateway:DELETE", tags},
		} {
			if got := g.pairs(op); len(got) != 1 || got[0] != w {
				t.Errorf("%s asked about %v, want exactly %v", op, got, w)
			}
		}
	}, kerneltest.Authorizer(g))
}
