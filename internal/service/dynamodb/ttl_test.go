package dynamodb_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"net/http"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// settle waits for the TTL sweeps the last call or advance started.
func (h *harness) settle() {
	h.Helper()
	if _, err := h.Kernel.Settle(h.Context(), time.Minute); err != nil {
		h.Fatal(err)
	}
}

// epoch is the clock's now in Unix seconds, exactly.
func (h *harness) epoch() *big.Rat {
	now := h.Clock.Now().Wire()
	r := big.NewRat(now.Unix(), 1)
	return r.Add(r, big.NewRat(int64(now.Nanosecond()), int64(time.Second)))
}

// lockTable creates table, keyed by id, with TTL on the attribute exp.
func (h *harness) lockTable(table string) {
	h.Helper()
	h.mustCall("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"`+table+
		`","KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`)
	h.mustCall("UpdateTimeToLive", `{"TableName":"`+table+`","TimeToLiveSpecification":{"AttributeName":"exp","Enabled":true}}`)
}

// expiry is what an item's TTL attribute holds: a number of epoch seconds,
// a string, which never expires, or nothing.
type expiry struct {
	// value is the attribute in DynamoDB JSON; empty for none.
	value string
	// at is the number's value; nil for a string or none.
	at *big.Rat
	// ancient is a number more than five years before the instant it was
	// written, which never expires.
	ancient bool
}

// expires reports whether e has expired at now.
func (e expiry) expires(now *big.Rat) bool {
	return e.at != nil && !e.ancient && e.at.Cmp(now) <= 0
}

// drawExpiry draws an expiry: mostly around the clock's now, up to 30 s
// past to 2 min ahead with millisecond digits, sometimes years back, on
// either side of the five-year bound.
func (h *harness) drawExpiry(rt *rapid.T) expiry {
	now := h.epoch()
	var offset *big.Rat
	switch rapid.IntRange(0, 7).Draw(rt, "kind") {
	case 0:
		return expiry{}
	case 1:
		return expiry{value: `{"S":"tomorrow"}`}
	case 2:
		offset = big.NewRat(-int64(rapid.IntRange(1, 157_700_000).Draw(rt, "pastSeconds")), 1)
	case 3:
		offset = big.NewRat(-int64(rapid.IntRange(157_900_000, 400_000_000).Draw(rt, "ancientSeconds")), 1)
	default:
		offset = big.NewRat(int64(rapid.IntRange(-30000, 120000).Draw(rt, "offsetMs")), 1000)
	}
	at := new(big.Rat).Add(now, offset)
	floor := big.NewRat(h.Clock.Now().Wire().AddDate(-5, 0, 0).Unix(), 1)
	return expiry{value: `{"N":"` + at.FloatString(3) + `"}`, at: at, ancient: at.Cmp(floor) < 0}
}

// TestTTLDeletesItemsExactlyWhenTheyExpire is a state machine of puts,
// refreshes and clock advances against a fake Local: after every step an
// item whose TTL attribute is a number at most the clock's now, and no
// more than five years before the instant it was written, is gone, and
// every other item is there as last written, whether its expiry came from
// a put or an update.
func TestTTLDeletesItemsExactlyWhenTheyExpire(t *testing.T) {
	runStore(t, func(h *harness, store *tableStore) {
		run := 0
		rapid.Check(bubbleTB{h.T}, func(rt *rapid.T) {
			run++
			table := fmt.Sprintf("Locks%d", run)
			h.lockTable(table)
			model := map[string]expiry{}
			ids := []string{"a", "b", "c", "d", "e"}
			rt.Repeat(map[string]func(*rapid.T){
				"put": func(rt *rapid.T) {
					id := rapid.SampledFrom(ids).Draw(rt, "id")
					e := h.drawExpiry(rt)
					attr := ""
					if e.value != "" {
						attr = `,"exp":` + e.value
					}
					h.mustCall("PutItem", `{"TableName":"`+table+`","Item":{"id":{"S":"`+id+`"}`+attr+`}}`)
					model[id] = e
				},
				"refresh": func(rt *rapid.T) {
					id := rapid.SampledFrom(ids).Draw(rt, "id")
					e := h.drawExpiry(rt)
					if e.value == "" {
						rt.Skip("an update sets a value")
					}
					h.mustCall("UpdateItem", `{"TableName":"`+table+`","Key":{"id":{"S":"`+id+`"}},"UpdateExpression":"SET #e = :v",`+
						`"ExpressionAttributeNames":{"#e":"exp"},"ExpressionAttributeValues":{":v":`+e.value+`}}`)
					model[id] = e
				},
				"advance": func(rt *rapid.T) {
					d := time.Duration(rapid.IntRange(0, 90000).Draw(rt, "advanceMs")) * time.Millisecond
					if _, err := h.Clock.Advance(d); err != nil {
						rt.Fatal(err)
					}
				},
				"": func(rt *rapid.T) {
					h.settle()
					now := h.epoch()
					maps.DeleteFunc(model, func(_ string, e expiry) bool { return e.expires(now) })
					got := map[string]string{}
					for _, it := range store.items(table) {
						var id struct{ S string }
						if err := json.Unmarshal(it["id"], &id); err != nil {
							rt.Fatal(err)
						}
						got[id.S] = string(it["exp"])
					}
					if !slices.Equal(slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(model))) {
						rt.Fatalf("at %s the table holds %v, want %v", now.FloatString(3), got, model)
					}
					for id, e := range model {
						if got[id] != e.value {
							rt.Fatalf("item %s holds exp %s, want %s", id, got[id], e.value)
						}
					}
				},
			})
			h.mustCall("DeleteTable", `{"TableName":"`+table+`"}`)
		})
	})
}

// TestRefreshedItemSurvivesTheSweep: an item refreshed between the sweep's
// scan and its delete stays, because the delete is conditioned on the
// item still being expired, so the refresh wins as on AWS
// (dynamodb-local.md 5.1); an item whose expiry lies more than five years
// in the past, such as a lock released by setting it to 0, is never
// deleted.
func TestRefreshedItemSurvivesTheSweep(t *testing.T) {
	runStore(t, func(h *harness, store *tableStore) {
		h.lockTable("Locks")
		h.settle()
		// Written behind the shim's back, so no sweep is due for them yet:
		// two expired a minute before the clock's 2000-01-01, one in 1970.
		for id, exp := range map[string]string{"held": "946684740", "free": "946684740", "released": "0"} {
			store.serve("PutItem", `{"TableName":"Locks","Item":{"id":{"S":"`+id+`"},"exp":{"N":"`+exp+`"}}}`)
		}
		store.beforeDelete = func(key item) {
			if string(key["id"]) == `{"S":"held"}` {
				store.serve("PutItem", `{"TableName":"Locks","Item":{"id":{"S":"held"},"exp":{"N":"99999999999"}}}`)
			}
		}
		deleted, err := h.svc.SweepTTL(h.Context())
		if err != nil || deleted != 1 {
			h.Fatalf("SweepTTL = %d, %v; want 1 deleted", deleted, err)
		}
		items := store.items("Locks")
		if len(items) != 2 || string(items[`{"id":{"S":"held"}}`]["exp"]) != `{"N":"99999999999"}` || string(items[`{"id":{"S":"released"}}`]["exp"]) != `{"N":"0"}` {
			h.Errorf("items after the sweep = %v, want the refreshed one and the one expired in 1970", items)
		}
	})
}

// TestTTLOnAKeyAttributeSweeps: a table keyed by its TTL attribute is
// swept like any other. The sweep's projection names the attribute once,
// as Local refuses overlapping paths.
func TestTTLOnAKeyAttributeSweeps(t *testing.T) {
	runStore(t, func(h *harness, store *tableStore) {
		h.mustCall("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"exp","AttributeType":"N"}],"TableName":"Expiring",`+
			`"KeySchema":[{"AttributeName":"exp","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`)
		h.mustCall("UpdateTimeToLive", `{"TableName":"Expiring","TimeToLiveSpecification":{"AttributeName":"exp","Enabled":true}}`)
		// The clock reads 2000-01-01T00:00:00Z: one expired a minute ago,
		// one expires in a day.
		h.mustCall("PutItem", `{"TableName":"Expiring","Item":{"exp":{"N":"946684740"}}}`)
		h.mustCall("PutItem", `{"TableName":"Expiring","Item":{"exp":{"N":"946771200"}}}`)
		h.settle()
		if deleted, err := h.svc.SweepTTL(h.Context()); err != nil {
			h.Fatalf("SweepTTL = %d, %v; want no error", deleted, err)
		}
		got := slices.Sorted(maps.Keys(store.items("Expiring")))
		if want := []string{`{"exp":{"N":"946771200"}}`}; !slices.Equal(got, want) {
			h.Errorf("items = %v, want %v", got, want)
		}
	})
}

// TestExpiriesOlderThanFiveYearsNeverExpire: an item put or updated with
// an expiry more than five years before now is kept however far the clock
// runs, while one inside the five years is deleted at once.
func TestExpiriesOlderThanFiveYearsNeverExpire(t *testing.T) {
	runStore(t, func(h *harness, store *tableStore) {
		h.lockTable("Locks")
		// The clock reads 2000-01-01T00:00:00Z; 1995-01-01 is five calendar
		// years back.
		h.mustCall("PutItem", `{"TableName":"Locks","Item":{"id":{"S":"old"},"exp":{"N":"788918399"}}}`)
		h.mustCall("PutItem", `{"TableName":"Locks","Item":{"id":{"S":"edge"},"exp":{"N":"788918400"}}}`)
		h.mustCall("PutItem", `{"TableName":"Locks","Item":{"id":{"S":"updated"}}}`)
		h.mustCall("UpdateItem", `{"TableName":"Locks","Key":{"id":{"S":"updated"}},"UpdateExpression":"SET #e = :v",`+
			`"ExpressionAttributeNames":{"#e":"exp"},"ExpressionAttributeValues":{":v":{"N":"0"}}}`)
		h.settle()
		if _, err := h.Clock.Advance(24 * time.Hour); err != nil {
			h.Fatal(err)
		}
		h.settle()
		if deleted, err := h.svc.SweepTTL(h.Context()); err != nil || deleted != 0 {
			h.Errorf("SweepTTL = %d, %v; want nothing left to delete", deleted, err)
		}
		got := slices.Sorted(maps.Keys(store.items("Locks")))
		if want := []string{`{"id":{"S":"old"}}`, `{"id":{"S":"updated"}}`}; !slices.Equal(got, want) {
			h.Errorf("items = %v, want %v", got, want)
		}
	})
}

// TestUpdatesSweepOnlyWhenTheirItemExpires: an UpdateItem on a table with
// TTL enabled reads the item's expiry back and arms the sweep for it,
// instead of scanning the table at once.
func TestUpdatesSweepOnlyWhenTheirItemExpires(t *testing.T) {
	runStore(t, func(h *harness, store *tableStore) {
		h.lockTable("Locks")
		h.settle()
		scans := h.localSaw("Scan")
		h.mustCall("UpdateItem", `{"TableName":"Locks","Key":{"id":{"S":"a"}},"UpdateExpression":"SET #e = :v",`+
			`"ExpressionAttributeNames":{"#e":"exp"},"ExpressionAttributeValues":{":v":{"N":"946684860"}}}`)
		h.settle()
		if n := h.localSaw("Scan"); n != scans {
			h.Errorf("an UpdateItem setting an expiry a minute ahead started %d Scans, want none until it expires", n-scans)
		}
		if _, err := h.Clock.Advance(time.Minute); err != nil {
			h.Fatal(err)
		}
		h.settle()
		if items := store.items("Locks"); len(items) != 0 {
			h.Errorf("items a minute later = %v, want the updated item deleted", items)
		}
	})
}

// TestUpdateReadBackIsNoCall: the read-back of an UpdateItem's expiry is
// part of the write, as on AWS, which makes no such read: a fault rule for
// the next GetItem waits for the client's own, and the journal records no
// GetItem the client did not make.
func TestUpdateReadBackIsNoCall(t *testing.T) {
	runStore(t, func(h *harness, _ *tableStore) {
		h.lockTable("Locks")
		throttled := dynamodbapi.IdentityProvisionedThroughputExceededException().Shape
		if _, err := h.Faults.Add(fault.Rule{
			Match:   fault.Match{Operations: []ref.OperationID{dynamodbapi.OpGetItemRaw.Descriptor().ID}, Resource: fault.NewARNGlob("arn:aws:dynamodb:*:*:table/Locks")},
			Phase:   fault.Request,
			Action:  fault.Error{Shape: throttled},
			Trigger: fault.NextN{N: 1},
		}); err != nil {
			h.Fatal(err)
		}
		h.mustCall("UpdateItem", `{"TableName":"Locks","Key":{"id":{"S":"a"}},"UpdateExpression":"SET #e = :v",`+
			`"ExpressionAttributeNames":{"#e":"exp"},"ExpressionAttributeValues":{":v":{"N":"946684860"}}}`)
		h.settle()
		if n := h.localSaw("GetItem"); n != 1 {
			h.Errorf("Local received %d GetItem requests after the UpdateItem, want the read-back", n)
		}
		if got := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Operation: dynamodbapi.OpGetItemRaw.Descriptor().ID}).Events; len(got) != 0 {
			h.Errorf("journal holds %d GetItem calls before the client made one, want none", len(got))
		}
		got := h.call("GetItem", `{"TableName":"Locks","Key":{"id":{"S":"a"}}}`)
		if e := parseError(h, got.Body); got.Status != http.StatusBadRequest || e.Type != "com.amazonaws.dynamodb.v20120810#"+throttled.Name() {
			h.Errorf("the client's GetItem = %d %s, want the injected %s", got.Status, got.Body, throttled.Name())
		}
		if got := h.call("GetItem", `{"TableName":"Locks","Key":{"id":{"S":"a"}}}`); got.Status != http.StatusOK {
			h.Errorf("the second GetItem = %d %s, want the rule used up", got.Status, got.Body)
		}
	})
}

// TestTTLSweepRunsAsTheServiceThroughThePipeline: the sweep's calls are
// journaled as internal calls of dynamodb.amazonaws.com made by the TTL
// shim, its deletes conditioned on the item still being expired at the
// sweep's instant.
func TestTTLSweepRunsAsTheServiceThroughThePipeline(t *testing.T) {
	runStore(t, func(h *harness, _ *tableStore) {
		h.lockTable("Locks")
		h.mustCall("PutItem", `{"TableName":"Locks","Item":{"id":{"S":"a"},"exp":{"N":"946684830"}}}`)
		h.settle()
		if _, err := h.Clock.Advance(30 * time.Second); err != nil {
			h.Fatal(err)
		}
		h.settle()
		deletes := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Operation: dynamodbapi.OpDeleteItemRaw.Descriptor().ID}).Events
		if len(deletes) != 1 {
			h.Fatalf("journal holds %d DeleteItem calls, want 1", len(deletes))
		}
		call, isCall := deletes[0].Detail.(journal.APICall)
		if !isCall || call.Origin != (ref.Internal{Via: ref.DynamoDBTTL}) || call.Mode != "service:dynamodb.amazonaws.com" {
			h.Errorf("the delete was journaled as %+v, want an internal call of the TTL shim as dynamodb.amazonaws.com", deletes[0].Detail)
		}
		if at := deletes[0].At.Wire(); !at.Equal(time.Date(2000, 1, 1, 0, 0, 30, 0, time.UTC)) {
			h.Errorf("the delete was admitted at %s, want the expiry 2000-01-01T00:00:30Z", at)
		}
		var sent []forwarded
		for _, f := range h.local.requests() {
			if f.Op == "DeleteItem" {
				sent = append(sent, f)
			}
		}
		// 788918430 is 1995-01-01T00:00:30Z, five calendar years earlier.
		want := `{"TableName":"Locks","Key":{"id":{"S":"a"}},"ConditionExpression":"#ttl BETWEEN :floor AND :now","ExpressionAttributeNames":{"#ttl":"exp"},` +
			`"ExpressionAttributeValues":{":floor":{"N":"788918430"},":now":{"N":"946684830"}}}`
		if len(sent) != 1 || sent[0].Body != want {
			h.Errorf("Local received DeleteItem %v, want %s", sent, want)
		}
	})
}

// TestDisabledTTLAndDeletedTablesAreNotSwept: no sweep touches a table
// whose TTL was turned off or which was deleted, and the admin sweep
// reports nothing deleted.
func TestDisabledTTLAndDeletedTablesAreNotSwept(t *testing.T) {
	runStore(t, func(h *harness, store *tableStore) {
		h.lockTable("Off")
		h.lockTable("Gone")
		for _, table := range []string{"Off", "Gone"} {
			h.mustCall("PutItem", `{"TableName":"`+table+`","Item":{"id":{"S":"a"},"exp":{"N":"946684860"}}}`)
		}
		h.settle()
		h.mustCall("UpdateTimeToLive", `{"TableName":"Off","TimeToLiveSpecification":{"AttributeName":"exp","Enabled":false}}`)
		h.mustCall("DeleteTable", `{"TableName":"Gone"}`)
		scans := h.localSaw("Scan")
		if _, err := h.Clock.Advance(time.Hour); err != nil {
			h.Fatal(err)
		}
		h.settle()
		if deleted, err := h.svc.SweepTTL(h.Context()); err != nil || deleted != 0 {
			h.Errorf("SweepTTL = %d, %v; want nothing deleted", deleted, err)
		}
		if n := h.localSaw("Scan"); n != scans {
			h.Errorf("Local received %d Scans after TTL was disabled and the table deleted, want none", n-scans)
		}
		if len(store.items("Off")) != 1 {
			h.Errorf("the item of the table with TTL off was deleted")
		}
	})
}

// TestReadingWritesNeverSweep: reads and PartiQL selects on a table with
// TTL enabled start no sweep; a PartiQL update does.
func TestReadingWritesNeverSweep(t *testing.T) {
	runStore(t, func(h *harness, store *tableStore) {
		h.local.answer = func(op, body string) reply {
			if op == "ExecuteStatement" {
				return ok(map[string]any{"Items": []any{}})
			}
			return store.serve(op, body)
		}
		h.lockTable("Locks")
		h.settle()
		scans := h.localSaw("Scan")
		h.mustCall("GetItem", `{"TableName":"Locks","Key":{"id":{"S":"a"}}}`)
		h.mustCall("ExecuteStatement", `{"Statement":"SELECT * FROM \"Locks\""}`)
		h.settle()
		if n := h.localSaw("Scan"); n != scans {
			h.Errorf("reads started %d Scans, want none", n-scans)
		}
		h.mustCall("ExecuteStatement", `{"Statement":"update \"Locks\" SET exp = 1 WHERE id = 'a'"}`)
		h.settle()
		if n := h.localSaw("Scan"); n == scans {
			h.Error("a PartiQL UPDATE started no sweep")
		}
	})
}
