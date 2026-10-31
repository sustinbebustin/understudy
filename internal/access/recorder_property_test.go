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
// and the resources calls touch: objects of two buckets, and resources
// whose ARN carries a segment understudy mints beside ones that stay exact.
var (
	propServices  = []string{"s3", "sqs", "lambda", "sts", "kms", "secretsmanager", "sns", "logs"}
	propPrefixes  = []string{"s3", "sqs", "lambda", "sts", "iam", "s3-object-lambda", "kms", "secretsmanager", "sns", "logs"}
	propResources = []string{
		"arn:aws:s3:::b1", "arn:aws:s3:::b1/k", "arn:aws:s3:::b1/dir/*k?", "arn:aws:s3:::b2/k",
		queueARN, roleARN, "arn:aws:lambda:us-east-1:111111111111:function:worker",
		keyARN, propOtherKey, "arn:aws:kms:us-east-1:111111111111:alias/app",
		propSecret, propSecretAgain, "arn:aws:secretsmanager:us-east-1:111111111111:secret:plain",
		topicARN, propSubscription, propMapping, groupARN, streamARN,
	}
)

const (
	propOtherKey     = "arn:aws:kms:us-east-1:111111111111:key/0f0e0d0c-0b0a-4908-8706-050403020100"
	propSecret       = "arn:aws:secretsmanager:us-east-1:111111111111:secret:db-a1b2c3" //nolint:gosec // G101: an ARN, not a credential
	propSecretAgain  = "arn:aws:secretsmanager:us-east-1:111111111111:secret:db-Z9y8X7" //nolint:gosec // G101: an ARN, not a credential
	propSubscription = topicARN + ":0f1e2d3c-4b5a-4968-8778-695a4b3c2d1e"
	propMapping      = "arn:aws:lambda:us-east-1:111111111111:event-source-mapping:6b2f1c3a-1d2e-4f5a-8b6c-7d8e9f0a1b2c"
)

// mintedRule is how the policy renders a minted resource of propResources
// (ARCHITECTURE.md 17; the per-type formats are pinned by
// TestMintedPatternRendersEachResourceType): its pattern, and whether it
// is minted only when the use is AsExecutionRole (Lambda's log streams).
type mintedRule struct {
	pattern  string
	execOnly bool
}

var propMinted = map[string]mintedRule{
	keyARN:           {pattern: "arn:aws:kms:us-east-1:111111111111:key/*"},
	propOtherKey:     {pattern: "arn:aws:kms:us-east-1:111111111111:key/*"},
	propSecret:       {pattern: "arn:aws:secretsmanager:us-east-1:111111111111:secret:db-??????"},
	propSecretAgain:  {pattern: "arn:aws:secretsmanager:us-east-1:111111111111:secret:db-??????"},
	propSubscription: {pattern: topicARN + ":*"},
	propMapping:      {pattern: "arn:aws:lambda:us-east-1:111111111111:event-source-mapping:*"},
	streamARN:        {pattern: groupARN + ":log-stream:*", execOnly: true},
}

// propModes are the modes an identity's calls are recorded under.
var propModes = []access.Mode{access.Direct{}, access.AsCaller{}, access.AsExecutionRole{}}

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
	outcome := rapid.SampledFrom([]access.Outcome{access.Succeeded{}, access.Failed{Code: "ResourceNotFoundException"}}).Draw(t, "outcome")
	return access.Entry{
		Principal: worker, Mode: rapid.SampledFrom(propModes).Draw(t, "mode"), Service: rapid.SampledFrom(propServices).Draw(t, "service"),
		Actions: actions, Resources: resources, Outcome: outcome, At: t0,
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

// allows reports whether doc allows action on resource.
func allows(doc policydoc.Document, action, resource string) bool {
	_, ok := policydoc.Allows(doc, policydoc.Query{Action: action, Resource: resource}).(policydoc.Allowed)
	return ok
}

// recordedPair is one (action, resource) a call used: the resource as the
// call named it ("*" for none), the text the exact rule renders it as, and
// the text the policy renders it as.
type recordedPair struct {
	action, resource, exact, rendered string
}

// pairsOf returns the pairs e used: an action on "*" only when it is of
// its call's service and the call touched nothing of that service,
// otherwise on each touched resource of the action's service, an S3
// object as arn:aws:s3:::<bucket>/*, a minted resource as its pattern.
func pairsOf(e *access.Entry) []recordedPair {
	var out []recordedPair
	_, exec := e.Mode.(access.AsExecutionRole)
	for _, a := range e.Actions {
		var touched []aws.ARN
		for _, res := range e.Resources {
			if res.Service == resourceService(a) {
				touched = append(touched, res)
			}
		}
		if len(touched) == 0 && a.Prefix == e.Service {
			out = append(out, recordedPair{a.String(), "*", "*", "*"})
		}
		for _, res := range touched {
			exact := policydoc.Literal(res.String())
			if bucket, ok := objectBucket(res); ok {
				exact = "arn:aws:s3:::" + bucket + "/*"
			}
			rendered := exact
			if rule, ok := propMinted[res.String()]; ok && (exec || !rule.execOnly) {
				rendered = rule.pattern
			}
			out = append(out, recordedPair{a.String(), res.String(), exact, rendered})
		}
	}
	return out
}

// renderAll returns the document granting every pair, one statement each.
func renderAll(pairs map[[2]string]bool) (policydoc.Document, bool) {
	gs := make([]policydoc.Grant, 0, len(pairs))
	for p := range pairs {
		gs = append(gs, policydoc.Grant{Actions: []string{p[0]}, Resources: []string{p[1]}})
	}
	return policydoc.Render(gs)
}

// Policy grants what the entries used and nothing else (ARCHITECTURE.md
// 17): each used pair on its exact resource, arn:aws:s3:::<bucket>/* for
// objects, a minted resource as its pattern, "*" for an action of its
// call's service without a touched resource of that service; an action
// granted on "*" is in no other statement. Checked as:
//
//	P1 it allows every recorded (action, resource) pair;
//	P2 on a resource of a kind never minted, or on "*", it allows nothing
//	   the exact rendering of the same record would not;
//	P3 every statement resource is an exact rendering or the pattern of a
//	   recorded minted use (the grants equal the model's);
//	P4 an action granted on "*" without a condition is in no other
//	   statement;
//	P5 each bucket's objects appear once.
func TestPolicyGrantsExactlyTheUsedPairsProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		entries := rapid.SliceOfN(rapid.Custom(propEntry), 1, 6).Draw(t, "entries")
		var r access.Recorder
		var used []recordedPair
		exact := map[[2]string]bool{}
		rendered := map[[2]string]bool{}
		buckets := map[string]bool{}
		for i := range entries {
			e := &entries[i]
			if err := r.Record(*e); err != nil {
				t.Fatal(err)
			}
			for _, p := range pairsOf(e) {
				used = append(used, p)
				exact[[2]string{p.action, p.exact}] = true
				rendered[[2]string{p.action, p.rendered}] = true
				if strings.HasPrefix(p.exact, "arn:aws:s3:::") && strings.HasSuffix(p.exact, "/*") {
					buckets[p.exact] = true
				}
			}
		}
		star := map[string]bool{}
		for p := range rendered {
			if p[1] == "*" {
				star[p[0]] = true
			}
		}
		want := map[[2]string]bool{}
		for p := range rendered {
			if p[1] == "*" || !star[p[0]] {
				want[p] = true
			}
		}

		doc, ok := r.Policy(worker)
		if ok != (len(want) > 0) {
			t.Fatalf("Policy reports a policy: %t, want %t (%d used pairs)", ok, len(want) > 0, len(want))
		}
		if !ok {
			return
		}

		// P1.
		for _, p := range used {
			if !allows(doc, p.action, p.resource) {
				t.Errorf("P1: Policy does not allow %s on %s, which an entry used", p.action, p.resource)
			}
		}

		// P2: the exact rendering, as the policy was before minted
		// segments and the "*" rule.
		exactDoc, _ := renderAll(exact)
		var actions []string
		for _, prefix := range propPrefixes {
			actions = append(actions, prefix+":Get", prefix+":Put")
		}
		queries := []string{"*"}
		for _, res := range propResources {
			if _, minted := propMinted[res]; !minted {
				queries = append(queries, res)
			}
		}
		for _, a := range actions {
			for _, res := range queries {
				if allows(doc, a, res) && !allows(exactDoc, a, res) {
					t.Errorf("P2: Policy allows %s on %s, which the record does not cover", a, res)
				}
			}
		}

		// P3.
		got := grants(t, doc)
		for pair := range got {
			if !want[pair] {
				t.Errorf("P3: Policy grants %s on %s, want only exact renderings and minted patterns of the record less actions granted on *", pair[0], pair[1])
			}
		}
		for pair := range want {
			if !got[pair] {
				t.Errorf("P3: Policy lacks %s on %s", pair[0], pair[1])
			}
		}

		// P4.
		for i, st := range doc.Statements {
			as, rs := renderedLists(t, st)
			if len(st.Condition) != 0 || !slices.Equal([]string(rs), []string{"*"}) {
				continue
			}
			for j, other := range doc.Statements {
				oas, _ := renderedLists(t, other)
				for _, a := range as {
					if j != i && slices.Contains(oas, a) {
						t.Errorf("P4: %s is granted on * by statement %d and again by statement %d", a, i, j)
					}
				}
			}
		}

		// P5.
		var objectLevel []string
		for _, st := range doc.Statements {
			_, rs := renderedLists(t, st)
			for _, res := range rs {
				if buckets[res] {
					objectLevel = append(objectLevel, res)
				}
			}
		}
		slices.Sort(objectLevel)
		if len(slices.Compact(slices.Clone(objectLevel))) != len(objectLevel) {
			t.Errorf("P5: Policy names object resources %v, want each bucket's once", objectLevel)
		}
	})
}

// renderedLists returns a rendered statement's Action and Resource, which
// policydoc.Render always writes as lists.
func renderedLists(t *rapid.T, st policydoc.Statement) (policydoc.Action, policydoc.Resource) {
	t.Helper()
	as, actionOK := st.Action.(policydoc.Action)
	rs, resourceOK := st.Resource.(policydoc.Resource)
	if !actionOK || !resourceOK {
		t.Fatalf("rendered statement has Action %T and Resource %T, want policydoc.Action and policydoc.Resource", st.Action, st.Resource)
	}
	return as, rs
}
