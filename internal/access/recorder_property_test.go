package access_test

import (
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The operations' services, the action prefixes their lists may hold
// (s3-object-lambda has no resource of its own here, and iam only the role)
// and the resources calls touch, objects of two buckets among them.
var (
	propServices  = []string{"s3", "sqs", "lambda", "sts"}
	propPrefixes  = []string{"s3", "sqs", "lambda", "sts", "iam", "s3-object-lambda"}
	propResources = []string{
		"arn:aws:s3:::b1", "arn:aws:s3:::b1/k", "arn:aws:s3:::b1/dir/*k?", "arn:aws:s3:::b2/k",
		queueARN, roleARN, "arn:aws:lambda:us-east-1:111111111111:function:worker",
	}
)

func propEntry(t *rapid.T) access.Entry {
	actions := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) registry.IAMAction {
		return act(rapid.SampledFrom(propPrefixes).Draw(t, "prefix"), rapid.SampledFrom([]string{"Get", "Put"}).Draw(t, "name"))
	}), 1, 4).Draw(t, "actions")
	names := rapid.SliceOfN(rapid.SampledFrom(propResources), 0, 3).Draw(t, "resources")
	resources := make([]aws.ARN, 0, len(names))
	for _, s := range names {
		a, err := aws.ParseARN(s)
		if err != nil {
			t.Fatal(err)
		}
		resources = append(resources, a)
	}
	return access.Entry{
		Principal: app, Mode: access.Direct{}, Service: rapid.SampledFrom(propServices).Draw(t, "service"),
		Actions: actions, Resources: resources, Outcome: access.Succeeded{}, At: t0,
	}
}

// resourceService is the service whose resources an action is authorized
// against: STS actions act on IAM roles.
func resourceService(a registry.IAMAction) string {
	if a.Prefix == "sts" {
		return "iam"
	}
	return a.Prefix
}

// objectBucket returns the bucket of an S3 object ARN.
func objectBucket(a aws.ARN) (string, bool) {
	if a.Service != "s3" {
		return "", false
	}
	bucket, _, ok := strings.Cut(a.Resource, "/")
	return bucket, ok
}

// grants returns the (action, resource) pairs of doc's statements.
func grants(t *rapid.T, doc policydoc.Document) map[[2]string]bool {
	out := map[[2]string]bool{}
	for _, st := range doc.Statements {
		as, ok := st.Action.(policydoc.Action)
		rs, rok := st.Resource.(policydoc.Resource)
		if !ok || !rok {
			t.Fatalf("statement %+v is not Action on Resource", st)
		}
		for _, a := range as {
			for _, r := range rs {
				out[[2]string{a, r}] = true
			}
		}
	}
	return out
}

// Policy grants only what the entries used: an action on "*" only when it
// is of its call's service and the call touched nothing of that service,
// otherwise on a touched resource of the action's service, each S3 object
// as arn:aws:s3:::<bucket>/*; it grants every such use; and it names each
// bucket's objects once.
func TestPolicyGrantsExactlyTheUsedPairsProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		entries := rapid.SliceOfN(rapid.Custom(propEntry), 1, 6).Draw(t, "entries")
		var r access.Recorder
		want := map[[2]string]bool{}
		buckets := map[string]bool{}
		for i := range entries {
			e := &entries[i]
			if err := r.Record(*e); err != nil {
				t.Fatal(err)
			}
			for _, a := range e.Actions {
				var touched []aws.ARN
				for _, res := range e.Resources {
					if res.Service == resourceService(a) {
						touched = append(touched, res)
					}
				}
				if len(touched) == 0 && a.Prefix == e.Service {
					want[[2]string{a.String(), "*"}] = true
				}
				for _, res := range touched {
					if bucket, ok := objectBucket(res); ok {
						want[[2]string{a.String(), "arn:aws:s3:::" + bucket + "/*"}] = true
						buckets[bucket] = true
						continue
					}
					want[[2]string{a.String(), policydoc.Literal(res.String())}] = true
				}
			}
		}

		doc, ok := r.Policy(app)
		if ok != (len(want) > 0) {
			t.Fatalf("Policy reports a policy: %t, want %t (%d used pairs)", ok, len(want) > 0, len(want))
		}
		if !ok {
			return
		}
		got := grants(t, doc)
		for pair := range got {
			if !want[pair] {
				t.Errorf("Policy grants %s on %s, which no entry used", pair[0], pair[1])
			}
		}
		for pair := range want {
			if !got[pair] {
				t.Errorf("Policy lacks %s on %s, which an entry used", pair[0], pair[1])
			}
		}
		var objectLevel []string
		for pair := range got {
			if strings.HasPrefix(pair[1], "arn:aws:s3:::") && strings.Contains(pair[1], "/") && !slices.Contains(objectLevel, pair[1]) {
				objectLevel = append(objectLevel, pair[1])
			}
		}
		if len(objectLevel) != len(buckets) {
			t.Errorf("Policy names object resources %v, want one per bucket of %v", objectLevel, buckets)
		}
	})
}
