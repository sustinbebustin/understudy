package sns

import (
	"net/url"
	"regexp"
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// The links of an HTTP delivery keep the forms of sns-http-delivery.md
// 4.3 and 4.6 under /_understudy/sns/ (8.3.3): on the TLS listener as
// https://localhost:<port> while it serves, else on the calling client's
// endpoint.
func TestHTTPLinksBase(t *testing.T) {
	scope := aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}
	const (
		topic = "arn:aws:sns:us-east-1:000000000000:events"
		sub   = topic + ":2bcfbf39-05c3-41de-beaa-fcfcc21c8f55"
		token = "2336412f37" //nolint:gosec // G101: the documented example token, not a credential
		cert  = "SimpleNotificationService-0123456789abcdef0123456789abcdef.pem"
	)
	client := kernel.Endpoint{Scheme: "http", Host: "localhost:4566"}
	cases := []struct {
		name    string
		tlsPort func() (uint16, bool)
		base    string
	}{
		{"a TLS listener serving", func() (uint16, bool) { return 4567, true }, "https://localhost:4567/_understudy/sns/"},
		{"no TLS listener yet", func() (uint16, bool) { return 0, false }, "http://localhost:4566/_understudy/sns/"},
		{"never a TLS listener", nil, "http://localhost:4566/_understudy/sns/"},
	}
	for _, c := range cases {
		l := (&Service{tlsPort: c.tlsPort}).httpLinks(client)
		want := []struct{ got, want string }{
			{l.confirm(topic, token), c.base + "ConfirmSubscriptionLink?Action=ConfirmSubscription&TopicArn=" + topic + "&Token=" + token},
			{l.unsubscribe(sub), c.base + "UnsubscribeLink?Action=Unsubscribe&SubscriptionArn=" + sub},
			{l.cert(scope, cert), c.base + "SigningCert/aws/us-east-1/000000000000/" + cert},
		}
		for _, w := range want {
			if w.got != w.want {
				t.Errorf("%s: link %q, want %q", c.name, w.got, w.want)
			}
		}
	}
}

// awsName matches an AWS DNS suffix: a partition's, the aws top-level
// domain or execute-api.com (sns-http-delivery.md 7.3).
var awsName = regexp.MustCompile(`(?i)amazonaws\.com|amazonaws\.eu|amazonwebservices|api\.aws|c2s\.ic\.gov|sc2s\.sgov\.gov|aws\.scloud|adc-e\.uk|hci\.ic\.gov|execute-api\.com|\.aws(?:[/:?&\s]|$)`)

// Whatever the scope, ARNs, token and TLS port, an HTTP delivery's links
// on the TLS listener are https URLs on localhost:<port> that name no AWS
// host (sns-http-delivery.md 5.6, 8.8).
func TestHTTPLinksNameNoAWSHost(t *testing.T) {
	regions := []string{"us-east-1", "us-west-2", "eu-west-1", "ap-southeast-2", "me-central-1", "sa-east-1"}
	rapid.Check(t, func(t *rapid.T) {
		port := rapid.Uint16Range(1, 65535).Draw(t, "port")
		region, err := aws.KnownRegions().Parse(rapid.SampledFrom(regions).Draw(t, "region"))
		if err != nil {
			t.Fatal(err)
		}
		account, err := aws.ParseAccountID(rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"))
		if err != nil {
			t.Fatal(err)
		}
		scope := aws.Scope{Partition: aws.PartitionAWS(), Account: account, Region: region}
		topic := "arn:aws:sns:" + region.String() + ":" + account.String() + ":" + rapid.StringMatching(`[A-Za-z0-9_-]{1,256}`).Draw(t, "topic")
		sub := topic + ":" + rapid.StringMatching(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`).Draw(t, "subscription")
		token := rapid.StringMatching(`[0-9a-f]{288}`).Draw(t, "token")
		client := kernel.Endpoint{Scheme: "http", Host: rapid.SampledFrom([]string{"localhost:4566", "127.0.0.1:4566", "[::1]:4566"}).Draw(t, "client")}

		l := (&Service{tlsPort: func() (uint16, bool) { return port, true }}).httpLinks(client)
		wantHost := "localhost:" + strconv.Itoa(int(port))
		for _, link := range []string{l.confirm(topic, token), l.unsubscribe(sub), l.cert(scope, "SimpleNotificationService-0123456789abcdef0123456789abcdef.pem")} {
			u, err := url.Parse(link)
			if err != nil {
				t.Fatalf("link %q does not parse: %v", link, err)
			}
			if u.Scheme != "https" || u.Host != wantHost {
				t.Errorf("link %q is on %s://%s, want https://%s", link, u.Scheme, u.Host, wantHost)
			}
			if m := awsName.FindString(link); m != "" {
				t.Errorf("link %q names the AWS host %q", link, m)
			}
		}
	})
}
