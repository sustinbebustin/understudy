package aws_test

import (
	"encoding/json"
	"errors"
	"testing"

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

func TestPartitionRejectsUnservedNames(t *testing.T) {
	var p aws.Partition
	err := p.UnmarshalText([]byte("aws-cn"))
	var target *aws.PartitionError
	if !errors.As(err, &target) || target.Name != "aws-cn" {
		t.Errorf("UnmarshalText(aws-cn) error = %v, want *PartitionError", err)
	}
}
