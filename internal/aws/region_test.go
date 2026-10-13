package aws_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
)

func TestKnownRegionsAcceptRealNamesOnly(t *testing.T) {
	regions := aws.KnownRegions()
	for _, name := range []string{"us-east-1", "eu-west-1", "ap-southeast-7", "mx-central-1"} {
		r, err := regions.Parse(name)
		if err != nil || r.String() != name {
			t.Errorf("Parse(%q) = %q, %v; want the region", name, r, err)
		}
	}
	for _, name := range []string{"local-1", "us-east-99", "US-EAST-1", "", "us-east-1 "} {
		_, err := regions.Parse(name)
		var target *aws.RegionError
		if !errors.As(err, &target) || target.Name != name {
			t.Errorf("Parse(%q) error = %v, want *RegionError naming it", name, err)
		}
	}
}

// The advice must name the real configuration key: the top-level regions
// array, not a regions.extra object, which the loader rejects.
func TestUnknownRegionErrorNamesTheRegionsArray(t *testing.T) {
	_, err := aws.KnownRegions().Parse("mars-1")
	if err == nil {
		t.Fatal("Parse(mars-1) succeeded")
	}
	const want = `region "mars-1" is not an AWS region name; to use it, list it in the regions array of the configuration file (understudy.json by default), such as {"regions": ["mars-1"]}`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %s\nwant      %s", got, want)
	}
}

func TestWithExtraAddsNames(t *testing.T) {
	base := aws.KnownRegions()
	regions, err := base.WithExtra("local-1")
	if err != nil {
		t.Fatalf("WithExtra: %v", err)
	}
	if _, err := regions.Parse("local-1"); err != nil {
		t.Errorf("Parse(local-1) after WithExtra: %v", err)
	}
	if _, err := base.Parse("local-1"); err == nil {
		t.Error("WithExtra changed the receiver: base accepts local-1")
	}
	more, err := regions.WithExtra("local-2")
	if err != nil {
		t.Fatalf("WithExtra: %v", err)
	}
	if _, err := more.Parse("local-1"); err != nil {
		t.Errorf("a second WithExtra dropped local-1: %v", err)
	}
	if _, err := regions.Parse("local-2"); err == nil {
		t.Error("a second WithExtra changed its receiver: regions accepts local-2")
	}
}

func TestWithExtraRejectsMalformedNames(t *testing.T) {
	for _, name := range []string{"", "Local", "local_1", "-local", "local-", "1local"} {
		_, err := aws.KnownRegions().WithExtra(name)
		var target *aws.RegionError
		if !errors.As(err, &target) || !target.Malformed {
			t.Errorf("WithExtra(%q) error = %v, want a malformed *RegionError", name, err)
		}
	}
}

func TestRegionJSONRoundTrip(t *testing.T) {
	type bucket struct{ Region aws.Region }
	in := bucket{Region: aws.DefaultRegion()}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != `{"Region":"us-east-1"}` {
		t.Fatalf("Marshal = %s", data)
	}
	var out bucket
	if err := json.Unmarshal(data, &out); err != nil || out != in {
		t.Fatalf("Unmarshal = %+v, %v; want %+v", out, err, in)
	}
	if err := json.Unmarshal([]byte(`{"Region":"Not A Region"}`), &out); err == nil {
		t.Error("Unmarshal of a malformed region succeeded")
	}
}
