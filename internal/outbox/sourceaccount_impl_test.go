package outbox

import (
	"reflect"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// A pending delivery keeps the aws:SourceAccount it sends across a
// snapshot, a zero one included: a reloaded S3 notification still sends
// the bucket owner's account (iam-enforcement-surfaces.md 8.1).
func TestStateRoundTripKeepsTheSourceAccount(t *testing.T) {
	c := codec()
	rapid.Check(t, func(t *rapid.T) {
		e := genEntry().Draw(t, "entry")
		if rapid.Bool().Draw(t, "sends") {
			account, err := aws.ParseAccountID(rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"))
			if err != nil {
				t.Fatal(err)
			}
			e.Request.SourceAccount = account
		}
		st := newState(aws.Scope{})
		st.Entries[e.ID] = e
		data, err := c.Marshal(&st)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		got := newState(aws.Scope{})
		if err := c.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		if !reflect.DeepEqual(got, st) {
			t.Fatalf("round trip of %s:\ngot  %+v\nwant %+v", data, got, st)
		}
	})
}
