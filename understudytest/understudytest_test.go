package understudytest_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
	"github.com/sustinbebustin/understudy/understudytest"
)

func createQueue(t *testing.T, endpoint, name string) {
	t.Helper()
	if code := createQueueIn(t, endpoint, name, "us-east-1"); code != http.StatusOK {
		t.Fatalf("CreateQueue %s = %d", name, code)
	}
}

// createQueueIn sends a CreateQueue signed for region and returns the
// status code.
func createQueueIn(t *testing.T, endpoint, name, region string) int {
	t.Helper()
	body := `{"QueueName":"` + name + `"}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	table, err := aws.KnownRegions().WithExtra("local-1")
	if err != nil {
		t.Fatal(err)
	}
	r, err := table.Parse(region)
	if err != nil {
		t.Fatal(err)
	}
	auth.Sign(req, []byte(body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: r, Service: "sqs"}, time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

// Options.Regions adds region names beyond AWS's own: a queue in local-1
// is created on an instance that has it, refused on one that does not.
func TestExtraRegions(t *testing.T) {
	with := understudytest.Start(t, understudytest.Options{Docker: understudytest.DockerDisabled{}, Regions: []string{"local-1"}})
	if code := createQueueIn(t, with.Endpoint(), "q", "local-1"); code != http.StatusOK {
		t.Errorf("CreateQueue in local-1 with Regions = %d, want 200", code)
	}
	without := understudytest.Start(t, understudytest.Options{Docker: understudytest.DockerDisabled{}})
	if code := createQueueIn(t, without.Endpoint(), "q", "local-1"); code == http.StatusOK {
		t.Errorf("CreateQueue in local-1 without Regions = %d, want a refusal", code)
	}
}

// The Docker and DynamoDB options reach the server: with a Docker that
// answers, health reports DynamoDB disabled by configuration.
func TestDockerAndDynamoDBOptions(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{})
	inst := understudytest.Start(t, understudytest.Options{Docker: understudytest.DockerSocket{Path: d.Socket()}, DynamoDB: understudytest.DynamoDBDisabled{}})
	h, err := inst.Admin().Health(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if h.Docker.Status != admin.Enabled || h.DynamoDBLocal.Status != admin.Disabled || !strings.Contains(h.DynamoDBLocal.Reason, "configuration") {
		t.Errorf("health docker %+v, dynamodb local %+v; want Docker enabled and DynamoDB disabled by configuration", h.Docker, h.DynamoDBLocal)
	}
}

// Two instances in parallel tests each serve their own state: a queue
// created in one is not listed by the other.
func TestParallelInstancesAreIsolated(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"alpha", "beta"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inst := understudytest.Start(t, understudytest.Options{
				Docker: understudytest.DockerDisabled{},
			})
			createQueue(t, inst.Endpoint(), name)
			res, err := inst.Admin().Resources(t.Context(), admin.ResourceQuery{Services: []string{"sqs"}})
			if err != nil {
				t.Fatal(err)
			}
			if len(res) != 1 || !strings.HasSuffix(res[0].ARN, ":"+name) {
				t.Errorf("instance %s lists %+v, want only its own queue", name, res)
			}
		})
	}
}

// A seeded, frozen instance reports the frozen time, settles, and hands out
// an environment that points at itself.
func TestFrozenInstance(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	inst := understudytest.Start(t, understudytest.Options{
		Seed:   new(uint64(7)),
		Clock:  understudytest.Frozen{At: at},
		Docker: understudytest.DockerDisabled{},
	})
	clock, err := inst.Admin().Clock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !clock.Now.Equal(at) || clock.Mode != admin.Frozen {
		t.Errorf("clock = %+v, want frozen at %s", clock, at)
	}
	if err := inst.Settle(t.Context()); err != nil {
		t.Errorf("Settle on an idle instance: %v", err)
	}
	env := inst.Env()
	if env["AWS_ENDPOINT_URL"] != inst.Endpoint() || env["AWS_ACCESS_KEY_ID"] != "test" || env["AWS_REGION"] != "us-east-1" {
		t.Errorf("Env() = %v", env)
	}
}
