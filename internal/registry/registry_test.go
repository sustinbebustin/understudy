package registry_test

import (
	"errors"
	"testing"

	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// service builds a descriptor the way the generator does: operations sorted
// by name, Ordinal equal to the index.
func service(id string, ops ...string) *registry.Service {
	s := &registry.Service{ID: ref.ServiceID(id), SigningName: id}
	for i, name := range ops {
		s.Operations = append(s.Operations, &registry.Operation{
			Service: s,
			ID:      ref.OperationID{Service: s.ID, Name: name},
			Ordinal: i,
			Kind:    registry.Modeled,
		})
	}
	return s
}

func wantProblem(t *testing.T, err error, p registry.DescriptorProblem) {
	t.Helper()
	var target *registry.DescriptorError
	if !errors.As(err, &target) || target.Problem != p {
		t.Fatalf("New error = %v, want problem %d", err, p)
	}
}

func TestNewRejectsDuplicateServiceIDs(t *testing.T) {
	a := service("sqs", "SendMessage")
	b := service("sqs", "ReceiveMessage")
	b.SigningName = "other"
	_, err := registry.New(a, b)
	wantProblem(t, err, registry.DuplicateService)
}

func TestNewRejectsNonContiguousOrdinals(t *testing.T) {
	s := service("sqs", "CreateQueue", "DeleteQueue", "SendMessage")
	s.Operations[2].Ordinal = 3
	_, err := registry.New(s)
	wantProblem(t, err, registry.OrdinalMismatch)

	s = service("sqs", "CreateQueue", "DeleteQueue")
	s.Operations[0].Ordinal, s.Operations[1].Ordinal = 1, 0
	_, err = registry.New(s)
	wantProblem(t, err, registry.OrdinalMismatch)
}

func TestNewRejectsInconsistentDescriptors(t *testing.T) {
	tests := []struct {
		name  string
		build func() []*registry.Service
		want  registry.DescriptorProblem
	}{
		{"nil service", func() []*registry.Service { return []*registry.Service{nil} }, registry.NilService},
		{"empty id", func() []*registry.Service { return []*registry.Service{service("")} }, registry.EmptyServiceID},
		{"shared signing name", func() []*registry.Service {
			a, b := service("s3"), service("s3control")
			b.SigningName = "s3"
			return []*registry.Service{a, b}
		}, registry.DuplicateSigningName},
		{"shared JSON target", func() []*registry.Service {
			a, b := service("a"), service("b")
			a.JSONTargetPrefix, b.JSONTargetPrefix = "AmazonSQS", "AmazonSQS"
			return []*registry.Service{a, b}
		}, registry.DuplicateJSONTarget},
		{"awsquery without a version", func() []*registry.Service {
			s := service("sns", "Publish")
			s.Protocols = []ref.ProtocolID{ref.AWSQuery}
			return []*registry.Service{s}
		}, registry.MissingQueryVersion},
		{"nil operation", func() []*registry.Service {
			s := service("sqs", "SendMessage")
			s.Operations = append(s.Operations, nil)
			return []*registry.Service{s}
		}, registry.NilOperation},
		{"operation of another service", func() []*registry.Service {
			s, other := service("sqs", "SendMessage"), service("sns", "Publish")
			s.Operations[0].Service = other
			return []*registry.Service{s}
		}, registry.ForeignOperation},
		{"operations out of order", func() []*registry.Service {
			return []*registry.Service{service("sqs", "SendMessage", "CreateQueue")}
		}, registry.OperationOrder},
		{"duplicate operation name", func() []*registry.Service {
			return []*registry.Service{service("sqs", "SendMessage", "SendMessage")}
		}, registry.OperationOrder},
		{"zero kind", func() []*registry.Service {
			s := service("sqs", "SendMessage")
			s.Operations[0].Kind = 0
			return []*registry.Service{s}
		}, registry.UnknownOperationKind},
		{"route to another service's operation", func() []*registry.Service {
			s, other := service("s3", "GetObject"), service("lambda", "Invoke")
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method:    "GET",
				Operation: other.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route to a copy of the service's own descriptor", func() []*registry.Service {
			s := service("s3", "GetObject")
			stale := *s.Operations[0]
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method:    "GET",
				Operation: &stale,
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route with two greedy labels", func() []*registry.Service {
			s := service("s3", "GetObject")
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method: "GET", Path: []registry.Segment{registry.Greedy{Name: "A"}, registry.Greedy{Name: "B"}},
				Operation: s.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route with an empty label name", func() []*registry.Service {
			s := service("s3", "GetObject")
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method: "GET", Path: []registry.Segment{registry.Label{Name: ""}},
				Operation: s.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route with an empty literal", func() []*registry.Service {
			s := service("s3", "GetObject")
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method: "GET", Path: []registry.Segment{registry.Literal{Value: ""}},
				Operation: s.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route with a nil segment", func() []*registry.Service {
			s := service("s3", "GetObject")
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method: "GET", Path: []registry.Segment{registry.Label{Name: "Bucket"}, nil},
				Operation: s.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route to an internal operation", func() []*registry.Service {
			s := service("sqs", "MoveTaskTick")
			s.Operations[0].Kind = registry.Internal
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method: "POST", Path: []registry.Segment{registry.Literal{Value: "tick"}},
				Operation: s.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route to a raw operation", func() []*registry.Service {
			s := service("lambda", "InvokeFunctionUrl")
			s.Operations[0].Kind = registry.Raw
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method: "POST", Operation: s.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route requiring a header with an empty name", func() []*registry.Service {
			s := service("s3", "CopyObject")
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method: "PUT", Path: []registry.Segment{registry.Label{Name: "Bucket"}, registry.Greedy{Name: "Key"}},
				Headers:   []string{""},
				Operation: s.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route requiring one header twice in different case", func() []*registry.Service {
			s := service("s3", "CopyObject")
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method: "PUT", Path: []registry.Segment{registry.Label{Name: "Bucket"}, registry.Greedy{Name: "Key"}},
				Headers:   []string{"x-amz-copy-source", "X-Amz-Copy-Source"},
				Operation: s.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route with a repeated query key", func() []*registry.Service {
			s := service("s3", "GetBucketAcl")
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method: "GET", Query: []registry.QueryMatch{registry.QueryPresent{Key: "acl"}, registry.QueryPresent{Key: "acl"}},
				Operation: s.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route with a query key both present and equal", func() []*registry.Service {
			s := service("s3", "ListObjectsV2")
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method: "GET", Query: []registry.QueryMatch{registry.QueryPresent{Key: "list-type"}, registry.QueryEquals{Key: "list-type", Value: "2"}},
				Operation: s.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route with a literal holding a slash", func() []*registry.Service {
			s := service("s3", "GetObject")
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method: "GET", Path: []registry.Segment{registry.Literal{Value: "a/b"}},
				Operation: s.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"route with an empty query key", func() []*registry.Service {
			s := service("s3", "GetObject")
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{{
				Method: "GET", Query: []registry.QueryMatch{registry.QueryPresent{Key: ""}},
				Operation: s.Operations[0],
			}}}
			return []*registry.Service{s}
		}, registry.InvalidRoute},
		{"two routes with the same pattern, label names aside", func() []*registry.Service {
			s := service("s3", "CopyObject", "PutObject")
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{
				{
					Method: "PUT", Path: []registry.Segment{registry.Label{Name: "Bucket"}, registry.Greedy{Name: "Key"}},
					Operation: s.Operations[1],
				},
				{
					Method: "PUT", Path: []registry.Segment{registry.Label{Name: "B"}, registry.Greedy{Name: "K"}},
					Operation: s.Operations[0],
				},
			}}
			return []*registry.Service{s}
		}, registry.ConflictingRoutes},
		{"two routes whose query and headers differ only in order and case", func() []*registry.Service {
			s := service("s3", "CopyObject", "PutObject")
			path := []registry.Segment{registry.Label{Name: "Bucket"}, registry.Greedy{Name: "Key"}}
			s.Routes = &registry.RouteTable{Routes: []*registry.Route{
				{
					Method: "PUT", Path: path, Query: []registry.QueryMatch{
						registry.QueryPresent{Key: "x-id"}, registry.QueryEquals{Key: "v", Value: "1"},
					},
					Headers:   []string{"x-amz-copy-source"},
					Operation: s.Operations[1],
				},
				{
					Method: "PUT", Path: path, Query: []registry.QueryMatch{
						registry.QueryEquals{Key: "v", Value: "1"}, registry.QueryPresent{Key: "x-id"},
					},
					Headers:   []string{"X-Amz-Copy-Source"},
					Operation: s.Operations[0],
				},
			}}
			return []*registry.Service{s}
		}, registry.ConflictingRoutes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := registry.New(tt.build()...)
			wantProblem(t, err, tt.want)
		})
	}
}

// Rows that share a template but differ in a header, a query condition, a
// segment kind or the method are distinguishable; Match ranks them.
func TestNewAcceptsDistinguishableRoutes(t *testing.T) {
	s := service("s3", "CopyObject", "GetBucketAcl", "GetBucketTagging", "GetObject", "ListObjectsV2", "PutObject")
	object := []registry.Segment{registry.Label{Name: "Bucket"}, registry.Greedy{Name: "Key"}}
	bucket := []registry.Segment{registry.Label{Name: "Bucket"}}
	route := func(method string, path []registry.Segment, query []registry.QueryMatch, headers []string, op int) *registry.Route {
		return &registry.Route{Method: method, Path: path, Query: query, Headers: headers, Operation: s.Operations[op]}
	}
	s.Routes = &registry.RouteTable{Routes: []*registry.Route{
		route("PUT", object, nil, []string{"x-amz-copy-source"}, 0),
		route("PUT", object, nil, nil, 5),
		route("GET", object, nil, nil, 3),
		route("GET", bucket, []registry.QueryMatch{registry.QueryPresent{Key: "acl"}}, nil, 1),
		route("GET", bucket, []registry.QueryMatch{registry.QueryPresent{Key: "tagging"}}, nil, 2),
		route("GET", bucket, []registry.QueryMatch{registry.QueryEquals{Key: "list-type", Value: "2"}}, nil, 4),
	}}
	if _, err := registry.New(s); err != nil {
		t.Fatalf("New: %v", err)
	}
}

func TestRegistryLookups(t *testing.T) {
	sqs := service("sqs", "CreateQueue", "HoldVisibility", "SendMessage")
	sqs.JSONTargetPrefix = "AmazonSQS"
	sqs.Protocols = []ref.ProtocolID{ref.AWSJSON10, ref.AWSQuery}
	sqs.QueryVersion = "2012-11-05"
	sqs.Operations[1].Kind = registry.Internal
	cw := service("cloudwatch", "PutMetricData")
	cw.SigningName = "monitoring"
	cw.Protocols = []ref.ProtocolID{ref.AWSJSON10, ref.AWSQuery, ref.RPCv2CBOR}
	cw.QueryVersion = "2010-08-01"

	r, err := registry.New(sqs, cw)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := r.Services(); len(got) != 2 || got[0] != cw || got[1] != sqs {
		t.Errorf("Services() = %v, want cloudwatch then sqs", got)
	}
	if s, ok := r.Service("sqs"); !ok || s != sqs {
		t.Errorf("Service(sqs) = %v, %v", s, ok)
	}
	if s, ok := r.ByJSONTarget("AmazonSQS"); !ok || s != sqs {
		t.Errorf("ByJSONTarget(AmazonSQS) = %v, %v", s, ok)
	}
	if s, ok := r.BySigningName("monitoring"); !ok || s != cw {
		t.Errorf("BySigningName(monitoring) = %v, %v", s, ok)
	}
	if _, ok := r.BySigningName("cloudwatch"); ok {
		t.Error("BySigningName(cloudwatch) found a service; CloudWatch signs as monitoring")
	}
	if op, ok := r.ByQueryAction("SendMessage", "2012-11-05"); !ok || op != sqs.Operations[2] {
		t.Errorf("ByQueryAction(SendMessage, 2012-11-05) = %v, %v", op, ok)
	}
	if _, ok := r.ByQueryAction("SendMessage", "2010-08-01"); ok {
		t.Error("ByQueryAction matched SQS's action under CloudWatch's version")
	}
	if _, ok := r.ByQueryAction("HoldVisibility", "2012-11-05"); ok {
		t.Error("ByQueryAction found an internal operation; the edge must never claim one")
	}
	if op, ok := r.Operation(ref.OperationID{Service: "sqs", Name: "HoldVisibility"}); !ok || op != sqs.Operations[1] {
		t.Errorf("Operation(sqs.HoldVisibility) = %v, %v", op, ok)
	}
	if _, ok := r.Operation(ref.OperationID{Service: "sqs", Name: "Missing"}); ok {
		t.Error("Operation(sqs.Missing) found an operation")
	}
	if _, ok := r.Operation(ref.OperationID{Service: "nope", Name: "SendMessage"}); ok {
		t.Error("Operation(nope.SendMessage) found an operation")
	}
}

func TestNewAcceptsNoServices(t *testing.T) {
	r, err := registry.New()
	if err != nil || len(r.Services()) != 0 {
		t.Fatalf("New() = %v, %v", r, err)
	}
}

func TestIAMActionString(t *testing.T) {
	if got := (registry.IAMAction{Prefix: "sqs", Name: "SendMessage"}).String(); got != "sqs:SendMessage" {
		t.Errorf("String() = %q", got)
	}
}
