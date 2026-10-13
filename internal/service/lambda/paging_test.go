package lambda_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/paging/pagingtest"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// markerRefused is understudy's text for a Marker no page of the listing
// issued.
func markerRefused(marker string) string {
	return "The marker " + marker + " is not one a previous page returned; start the listing again without a Marker."
}

// A Marker ListAliases issued, or base64 text no listing issued, is
// InvalidParameterValueException from ListFunctions, not an empty page.
func TestListFunctionsRefusesForeignMarkers(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.createFunction("f")
		for _, name := range []string{"a", "b"} {
			call(h, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{FunctionName: "f", Name: name, FunctionVersion: "$LATEST"})
		}
		aliases := call(h, lambdaapi.OpListAliases, &lambdaapi.ListAliasesInput{FunctionName: "f", MaxItems: new(int32(1))})
		if aliases.NextMarker == nil {
			t.Fatal("ListAliases MaxItems 1 of two aliases returned no NextMarker")
		}
		for _, marker := range []string{*aliases.NextMarker, "Zm9v"} {
			err := fail(h, lambdaapi.OpListFunctions, &lambdaapi.ListFunctionsInput{Marker: new(marker)})
			errorIs(t, err, "InvalidParameterValueException", markerRefused(marker))
		}
	})
}

func functionName(n int) string { return fmt.Sprintf("fn-%02d", n) }

// Paging through 25 functions ten at a time serves each once, in order.
func TestListFunctionsPagesServeEachOnce(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		for n := range 25 {
			h.createFunction(functionName(n))
		}
		var got []string
		in := &lambdaapi.ListFunctionsInput{MaxItems: new(int32(10))}
		for range 4 {
			out := call(h, lambdaapi.OpListFunctions, in)
			for i := range out.Functions {
				got = append(got, *out.Functions[i].FunctionName)
			}
			if in.Marker = out.NextMarker; in.Marker == nil {
				break
			}
		}
		if len(got) != 25 || in.Marker != nil {
			t.Fatalf("pages served %d functions and then NextMarker %v, want 25 and none: %v", len(got), in.Marker, got)
		}
		for n, name := range got {
			if name != functionName(n) {
				t.Fatalf("function %d served is %s, want %s: %v", n, name, functionName(n), got)
			}
		}
	})
}

// fn is the function the per-function listings work on.
const fn = "fn"

// qualifierOf is the qualifier ending a qualified function ARN.
func qualifierOf(arn string) string { return arn[strings.LastIndexByte(arn, ':')+1:] }

// createAlias points a new alias of fn at $LATEST.
func createAlias(h *harness, name string) {
	h.Helper()
	call(h, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{FunctionName: fn, Name: name, FunctionVersion: "$LATEST"})
}

// pagedListing is one Lambda listing, driven as a client drives it.
type pagedListing struct {
	op string
	of func(h *harness) pagingtest.Listing
}

// pagedListings are the six Lambda listings.
func pagedListings() []pagedListing {
	return []pagedListing{
		{"ListFunctions", func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				Add: func(name string) string { return *h.createFunction(name).FunctionName },
				Remove: func(id string) {
					call(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: id})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := call(h, lambdaapi.OpListFunctions, &lambdaapi.ListFunctionsInput{MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.Functions))
					for i := range out.Functions {
						ids[i] = *out.Functions[i].FunctionName
					}
					return ids, out.NextMarker
				},
			}
		}},
		{"ListVersionsByFunction", func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				// A version is published from a changed configuration, so
				// its number is the highest yet and it sorts ahead.
				Add: func(name string) string {
					call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: fn, Description: new(name)})
					return *call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: fn}).Version
				},
				Remove: func(id string) {
					call(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: fn, Qualifier: new(id)})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := call(h, lambdaapi.OpListVersionsByFunction, &lambdaapi.ListVersionsByFunctionInput{FunctionName: fn, MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.Versions))
					for i := range out.Versions {
						ids[i] = *out.Versions[i].Version
					}
					return ids, out.NextMarker
				},
			}
		}},
		{"ListAliases", func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				Add: func(name string) string {
					createAlias(h, name)
					return name
				},
				Remove: func(id string) {
					call(h, lambdaapi.OpDeleteAlias, &lambdaapi.DeleteAliasInput{FunctionName: fn, Name: id})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := call(h, lambdaapi.OpListAliases, &lambdaapi.ListAliasesInput{FunctionName: fn, MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.Aliases))
					for i := range out.Aliases {
						ids[i] = *out.Aliases[i].Name
					}
					return ids, out.NextMarker
				},
			}
		}},
		{"ListEventSourceMappings", func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				// Mappings list by UUID, so where a new one sorts is chance.
				Add: func(name string) string {
					in := &lambdaapi.CreateEventSourceMappingInput{FunctionName: fn, EventSourceArn: new(h.queue(name))}
					return *call(h, lambdaapi.OpCreateEventSourceMapping, in).UUID
				},
				Remove: func(id string) {
					call(h, lambdaapi.OpDeleteEventSourceMapping, &lambdaapi.DeleteEventSourceMappingInput{UUID: id})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := call(h, lambdaapi.OpListEventSourceMappings, &lambdaapi.ListEventSourceMappingsInput{FunctionName: new(fn), MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.EventSourceMappings))
					for i := range out.EventSourceMappings {
						ids[i] = *out.EventSourceMappings[i].UUID
					}
					return ids, out.NextMarker
				},
			}
		}},
		{"ListFunctionEventInvokeConfigs", func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				Add: func(name string) string {
					createAlias(h, name)
					in := &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: fn, Qualifier: new(name), MaximumRetryAttempts: new(int32(1))}
					return qualifierOf(*call(h, lambdaapi.OpPutFunctionEventInvokeConfig, in).FunctionArn)
				},
				Remove: func(id string) {
					call(h, lambdaapi.OpDeleteFunctionEventInvokeConfig, &lambdaapi.DeleteFunctionEventInvokeConfigInput{FunctionName: fn, Qualifier: new(id)})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := call(h, lambdaapi.OpListFunctionEventInvokeConfigs, &lambdaapi.ListFunctionEventInvokeConfigsInput{FunctionName: fn, MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.FunctionEventInvokeConfigs))
					for i := range out.FunctionEventInvokeConfigs {
						ids[i] = qualifierOf(*out.FunctionEventInvokeConfigs[i].FunctionArn)
					}
					return ids, out.NextMarker
				},
			}
		}},
		{"ListFunctionUrlConfigs", func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				Add: func(name string) string {
					createAlias(h, name)
					in := &lambdaapi.CreateFunctionUrlConfigInput{FunctionName: fn, Qualifier: new(name), AuthType: lambdaapi.FunctionUrlAuthTypeNone}
					return qualifierOf(call(h, lambdaapi.OpCreateFunctionUrlConfig, in).FunctionArn)
				},
				Remove: func(id string) {
					call(h, lambdaapi.OpDeleteFunctionUrlConfig, &lambdaapi.DeleteFunctionUrlConfigInput{FunctionName: fn, Qualifier: new(id)})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := call(h, lambdaapi.OpListFunctionUrlConfigs, &lambdaapi.ListFunctionUrlConfigsInput{FunctionName: fn, MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.FunctionUrlConfigs))
					for i := range out.FunctionUrlConfigs {
						ids[i] = qualifierOf(out.FunctionUrlConfigs[i].FunctionArn)
					}
					return ids, out.NextMarker
				},
			}
		}},
	}
}

// Every Lambda listing, paged one to three items at a time while the item
// each marker names, an item already served and a new item come and go
// between pages, serves each item once and skips none present throughout.
func TestListingsSurviveChangesBetweenPages(t *testing.T) {
	for _, l := range pagedListings() {
		for maxItems := int32(1); maxItems <= 3; maxItems++ {
			t.Run(fmt.Sprintf("%s/MaxItems=%d", l.op, maxItems), func(t *testing.T) {
				run(t, lambda.Options{}, func(h *harness) {
					// For ListFunctions, fn is one more function, present
					// throughout.
					h.createFunction(fn)
					pagingtest.SurvivesChanges(h, l.of(h), 8, maxItems)
				})
			})
		}
	}
}
