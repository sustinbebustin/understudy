package outbox

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// A Scheduler delivery's KmsKeyArn survives a snapshot, so a loaded retry
// still asks for the role session's kms:Decrypt (scheduler.md 9, 14.11);
// a delivery without one stores no key.
func TestSchedulerEntryKeyRoundTrip(t *testing.T) {
	c := codec()
	plain := newState(aws.Scope{})
	plain.Entries["d-1"] = schedulerEntry()
	data, err := c.Marshal(&plain)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if bytes.Contains(data, []byte(`"key"`)) {
		t.Errorf("snapshot of a delivery without a key holds one:\n%s", data)
	}

	key := aws.ARN{Partition: "aws", Service: "kms", Region: "us-east-1", Account: "000000000000", Resource: "key/1234abcd-12ab-34cd-56ef-1234567890ab"}
	st := newState(aws.Scope{})
	e := schedulerEntry()
	e.Request.Invocation.Key = &key
	st.Entries["d-1"] = e
	data, err = c.Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `"key":"` + key.String() + `"`; !bytes.Contains(data, []byte(want)) {
		t.Errorf("snapshot\n%s\ndoes not hold %s", data, want)
	}
	back := newState(aws.Scope{})
	if err := c.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, st) {
		t.Errorf("round trip =\n%+v\nwant\n%+v", back, st)
	}
}
