package aws_test

import (
	"encoding/json"
	"errors"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
)

func TestScopeJSONRoundTrip(t *testing.T) {
	in := aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"Partition":"aws","Account":"000000000000","Region":"us-east-1"}`; string(data) != want {
		t.Fatalf("Marshal = %s, want %s", data, want)
	}
	var out aws.Scope
	if err := json.Unmarshal(data, &out); err != nil || out != in {
		t.Fatalf("Unmarshal = %+v, %v; want %+v", out, err, in)
	}
}

// ParseScope keeps the defaults for empty parts and names the part it
// refuses.
func TestParseScope(t *testing.T) {
	defaults := aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}
	regions := aws.KnownRegions()
	if got, err := aws.ParseScope("", "", defaults, regions); err != nil || got != defaults {
		t.Errorf("ParseScope of nothing = %+v, %v; want the defaults", got, err)
	}
	got, err := aws.ParseScope("111111111111", "eu-west-1", defaults, regions)
	if err != nil || got.Account.String() != "111111111111" || got.Region.String() != "eu-west-1" {
		t.Errorf("ParseScope(111111111111, eu-west-1) = %+v, %v", got, err)
	}
	for _, c := range []struct {
		account, region string
		part            aws.ScopePart
	}{{"12", "", aws.ScopeAccount}, {"", "mars-1", aws.ScopeRegion}} {
		_, err := aws.ParseScope(c.account, c.region, defaults, regions)
		if se, ok := errors.AsType[*aws.ScopeError](err); !ok || se.Part != c.part {
			t.Errorf("ParseScope(%q, %q) = %v, want a ScopeError about the %s", c.account, c.region, err, c.part)
		}
	}
}

// ScopeOf reads the scope a regional ARN names and refuses an ARN whose
// partition is not served, whose account is not 12 digits or that has no
// region, naming the part.
func TestScopeOf(t *testing.T) {
	got, err := aws.ScopeOf(aws.ARN{Partition: "aws", Service: "sqs", Region: "eu-west-1", Account: "111111111111", Resource: "q"})
	if err != nil || got.Partition != aws.PartitionAWS() || got.Account.String() != "111111111111" || got.Region.String() != "eu-west-1" {
		t.Errorf("ScopeOf(sqs queue ARN) = %+v, %v", got, err)
	}
	for _, c := range []struct {
		arn  aws.ARN
		part aws.ScopePart
	}{
		{aws.ARN{Partition: "aws-cn", Region: "cn-north-1", Account: "111111111111"}, aws.ScopePartition},
		{aws.ARN{Region: "us-east-1", Account: "111111111111"}, aws.ScopePartition},
		{aws.ARN{Partition: "aws", Region: "us-east-1", Account: "aws"}, aws.ScopeAccount},
		{aws.ARN{Partition: "aws", Region: "us-east-1"}, aws.ScopeAccount},
		{aws.ARN{Partition: "aws", Account: "111111111111"}, aws.ScopeRegion},
		{aws.ARN{Partition: "aws", Region: "US_EAST", Account: "111111111111"}, aws.ScopeRegion},
	} {
		_, err := aws.ScopeOf(c.arn)
		if se, ok := errors.AsType[*aws.ScopeError](err); !ok || se.Part != c.part {
			t.Errorf("ScopeOf(%+v) = %v, want a ScopeError about the %s", c.arn, err, c.part)
		}
	}
}

// Any served scope survives a round trip through an ARN naming it.
func TestScopeOfRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		account := rapid.StringMatching(`[0-9]{12}`).Draw(t, "account")
		region := rapid.StringMatching(`[a-z]{2}(-[a-z]{2,9}){1,2}-[1-9]`).Draw(t, "region")
		got, err := aws.ScopeOf(aws.ARN{Partition: "aws", Service: "lambda", Region: region, Account: account, Resource: "function:f"})
		if err != nil || got.Account.String() != account || got.Region.String() != region {
			t.Fatalf("ScopeOf(%s, %s) = %+v, %v", account, region, got, err)
		}
	})
}

func TestPartitionRejectsUnservedNames(t *testing.T) {
	var p aws.Partition
	err := p.UnmarshalText([]byte("aws-cn"))
	var target *aws.PartitionError
	if !errors.As(err, &target) || target.Name != "aws-cn" {
		t.Errorf("UnmarshalText(aws-cn) error = %v, want *PartitionError", err)
	}
}
