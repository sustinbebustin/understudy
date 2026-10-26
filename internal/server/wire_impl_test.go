package server

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// buildTable builds every row of the wiring table on a test kernel and
// hands the construction and the built rows to fn.
func buildTable(t *testing.T, fn func(k *kerneltest.K, c *construction, built []wiredBuilt)) {
	t.Helper()
	all := apis()
	descs := make([]*registry.Service, len(all))
	for i, a := range all {
		descs[i] = a.desc
	}
	kerneltest.Run(t, func(k *kerneltest.K) {
		c := &construction{}
		built, err := c.build(k.Deps)
		if err != nil {
			k.Fatalf("building the wiring table = %v", err)
		}
		fn(k, c, built)
	}, kerneltest.Services(descs...))
}

// Each row builds the service of its own descriptor, in table order, and
// the services wire hands on by type are all recorded.
func TestWiringTableBuildsEachRowsService(t *testing.T) {
	buildTable(t, func(k *kerneltest.K, c *construction, built []wiredBuilt) {
		if len(built) != len(ServiceIDs()) {
			k.Fatalf("built %d services, want one per row: %v", len(built), ServiceIDs())
		}
		for i, b := range built {
			if b.desc.ID != ServiceIDs()[i] || b.svc.Name() != b.desc.ID {
				k.Errorf("row %d built %s for descriptor %s, want %s for %s", i, b.svc.Name(), b.desc.ID, ServiceIDs()[i], ServiceIDs()[i])
			}
		}
		if c.s3 == nil || c.dynamodb == nil || c.sns == nil || c.lambda == nil {
			k.Errorf("construction = %+v, want S3, DynamoDB, SNS and Lambda recorded", c)
		}
	})
}

// The resource-policy providers are found by interface among the built
// services and keyed by ARN namespace: today SQS, SNS and Lambda, as the
// switch they replace routed (ARCHITECTURE.md 16).
func TestWiredPolicyProvidersAreSQSSNSAndLambda(t *testing.T) {
	buildTable(t, func(k *kerneltest.K, _ *construction, built []wiredBuilt) {
		policies, err := policyProviders(built)
		if err != nil {
			k.Fatal(err)
		}
		namespaces := make([]string, 0, len(policies))
		for ns := range policies {
			namespaces = append(namespaces, ns)
		}
		slices.Sort(namespaces)
		if want := []string{"lambda", "sns", "sqs"}; !slices.Equal(namespaces, want) {
			k.Errorf("policy providers for %v, want %v", namespaces, want)
		}
		// A queue that does not exist has no policy to advise on.
		missing := aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: "absent"}
		if doc, ok := policies.ResourcePolicy(missing); ok || doc != nil {
			k.Errorf("ResourcePolicy(%s) = %v, %v; want nil, false", missing, doc, ok)
		}
	})
}

// The Lambda execution layer's asynchronous queue is collected right after
// Lambda (ARCHITECTURE.md 14.7), and the outbox first; the rest keep table
// order.
func TestCollectPutsTheAsyncQueueBesideLambda(t *testing.T) {
	buildTable(t, func(k *kerneltest.K, c *construction, built []wiredBuilt) {
		x, err := lambda.NewExecution(c.lambda, c.lambdaDeps, lambda.ExecutionOptions{NoPool: "Docker is disabled in this test"})
		if err != nil {
			k.Fatal(err)
		}
		defer func() {
			if err := x.Close(k.Context()); err != nil {
				k.Error(err)
			}
		}()
		views, _ := collect(outbox.New(), built, x)
		got := make([]ref.ServiceID, len(views))
		for i, v := range views {
			got[i] = v.Name()
		}
		want := []ref.ServiceID{"outbox"}
		for _, id := range ServiceIDs() {
			want = append(want, id)
			if id == "lambda" {
				want = append(want, "lambda-async")
			}
		}
		if !slices.Equal(got, want) {
			k.Errorf("collected %v, want %v", got, want)
		}
	})
}

// policyHolder is a built service that stores resource policies; only Name
// and ResourcePolicy are called.
type policyHolder struct {
	serveradmin.Service
	name ref.ServiceID
	doc  *policydoc.Document
}

func (p policyHolder) Name() ref.ServiceID { return p.name }

func (p policyHolder) ResourcePolicy(aws.ARN) (*policydoc.Document, bool) { return p.doc, true }

// stateOnly is a built service that stores no resource policy.
type stateOnly struct {
	serveradmin.Service
	name ref.ServiceID
}

func (s stateOnly) Name() ref.ServiceID { return s.name }

// A target is routed to the provider of its ARN's namespace, which need not
// be the service's id; a namespace no service provides has no policy.
func TestPolicySourcesRouteByARNNamespace(t *testing.T) {
	queues, topics := &policydoc.Document{}, &policydoc.Document{}
	built := []wiredBuilt{
		{&registry.Service{ID: "queues", ARNNamespace: "q"}, policyHolder{name: "queues", doc: queues}},
		{&registry.Service{ID: "plain", ARNNamespace: "p"}, stateOnly{name: "plain"}},
		{&registry.Service{ID: "topics", ARNNamespace: "t"}, policyHolder{name: "topics", doc: topics}},
	}
	policies, err := policyProviders(built)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		namespace string
		want      *policydoc.Document
		ok        bool
	}{
		{"q", queues, true},
		{"t", topics, true},
		{"p", nil, false},
		{"queues", nil, false},
	} {
		doc, ok := policies.ResourcePolicy(aws.ARN{Partition: "aws", Service: c.namespace, Resource: "r"})
		if doc != c.want || ok != c.ok {
			t.Errorf("ResourcePolicy in namespace %q = %p, %v; want %p, %v", c.namespace, doc, ok, c.want, c.ok)
		}
	}
}

// Two providers of one namespace would leave the advisory asking only one
// of them, so the wiring is refused, naming both.
func TestPolicySourcesRefuseTwoProvidersOfOneNamespace(t *testing.T) {
	built := []wiredBuilt{
		{&registry.Service{ID: "first", ARNNamespace: "shared"}, policyHolder{name: "first"}},
		{&registry.Service{ID: "plain", ARNNamespace: "shared"}, stateOnly{name: "plain"}},
		{&registry.Service{ID: "second", ARNNamespace: "shared"}, policyHolder{name: "second"}},
	}
	_, err := policyProviders(built)
	dup, ok := errors.AsType[*PolicySourceError](err)
	if !ok || dup.Namespace != "shared" || dup.Services != [2]ref.ServiceID{"first", "second"} {
		t.Fatalf("policyProviders = %v, want a *PolicySourceError for namespace shared naming first and second", err)
	}
	for _, want := range []string{`"shared"`, "first and second", "bug in understudy's wiring"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}
