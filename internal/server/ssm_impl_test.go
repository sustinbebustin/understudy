package server_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
)

// GET /resources, which `understudy resources` reads, lists each SSM
// parameter by ARN with its type and latest version (spec ticket 11).
func TestSSMResourcesListParameters(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	mustSSM(t, endpoint, "PutParameter", `{"Name":"/res/a","Type":"String","Value":"v"}`)
	mustSSM(t, endpoint, "PutParameter", `{"Name":"/res/a","Value":"w","Overwrite":true}`)
	res, err := c.Resources(t.Context(), admin.ResourceQuery{Services: []string{"ssm"}})
	if err != nil {
		t.Fatal(err)
	}
	want := admin.Resource{
		Service: "ssm", Account: "000000000000", Region: "us-east-1",
		ARN: "arn:aws:ssm:us-east-1:000000000000:parameter/res/a", Summary: "String, version 2",
	}
	if len(res) != 1 || res[0] != want {
		t.Errorf("resources = %+v, want [%+v]", res, want)
	}
}
