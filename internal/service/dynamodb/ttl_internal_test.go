package dynamodb

import (
	"math/big"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// TestEpochTextRoundTrips: an instant written as the sweep's :now reads
// back as the same instant, so a sweep due at an item's expiry compares
// equal to it.
func TestEpochTextRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		at := clock.FromWire(time.Unix(rapid.Int64Range(-62135596800, 253402300799).Draw(t, "sec"), rapid.Int64Range(0, 999999999).Draw(t, "nsec")))
		got, ok := epochInstant(dynamodbapi.AttributeValueMemberN{Value: epochText(at)})
		if !ok || got.Compare(at) != 0 {
			t.Fatalf("epochInstant(epochText(%s)) = %s, %v", at, got, ok)
		}
	})
}

// TestExpiryIsTheFirstNanosecondNotBeforeTheNumber: a TTL number is due at
// the earliest instant whose epoch seconds are at least the number, so the
// sweep at that instant finds it expired and no earlier sweep does.
func TestExpiryIsTheFirstNanosecondNotBeforeTheNumber(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		whole := rapid.Int64Range(-1e10, 1e10).Draw(t, "whole")
		digits := rapid.StringMatching(`[0-9]{0,15}`).Draw(t, "fraction")
		text := big.NewInt(whole).String()
		if digits != "" {
			text += "." + digits
		}
		if rapid.Bool().Draw(t, "exponent") {
			text += "e0"
		}
		n, _ := new(big.Rat).SetString(text)
		at, ok := epochInstant(dynamodbapi.AttributeValueMemberN{Value: text})
		if !ok {
			t.Fatalf("epochInstant(%s) found no instant", text)
		}
		due, _ := new(big.Rat).SetString(epochText(at))
		before, _ := new(big.Rat).SetString(epochText(at.Add(-time.Nanosecond)))
		if due.Cmp(n) < 0 || before.Cmp(n) >= 0 {
			t.Fatalf("epochInstant(%s) = %s (%s), not the first nanosecond at or after it", text, at, epochText(at))
		}
	})
}

// TestOnlyNumbersInRangeExpire: a string, a malformed number, one past
// the year 9999 (dynamodb-local.md 5.1) and one more than five years
// before now never expire.
func TestOnlyNumbersInRangeExpire(t *testing.T) {
	now := clock.FromWire(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	for _, v := range []dynamodbapi.AttributeValue{
		dynamodbapi.AttributeValueMemberS{Value: "1791434400"},
		dynamodbapi.AttributeValueMemberN{Value: "soon"},
		dynamodbapi.AttributeValueMemberN{Value: "1e300"},
		dynamodbapi.AttributeValueMemberN{Value: "0"},
		dynamodbapi.AttributeValueMemberN{Value: "1602417599"}, // 2020-10-11T11:59:59Z
		nil,
	} {
		if at, ok := expiryOf(v, now); ok {
			t.Errorf("expiryOf(%#v, %s) = %s, want no expiry", v, now, at)
		}
	}
}

// TestExpiryWindowIsFiveCalendarYears: a TTL number expires, seen at now,
// exactly when it lies at or after the same calendar instant five years
// earlier (docs.aws.amazon.com/amazondynamodb/latest/developerguide/
// time-to-live-ttl-before-you-start.html).
func TestExpiryWindowIsFiveCalendarYears(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := clock.FromWire(time.Unix(rapid.Int64Range(946684800, 4102444800).Draw(t, "now"), 0).UTC())
		floor := now.Wire().AddDate(-5, 0, 0)
		offset := rapid.Int64Range(-86400, 86400).Draw(t, "offsetFromFloor")
		value := floor.Unix() + offset
		_, ok := expiryOf(dynamodbapi.AttributeValueMemberN{Value: big.NewInt(value).String()}, now)
		if want := offset >= 0; ok != want {
			t.Fatalf("expiryOf(%d, %s) expires = %v, want %v (floor %s)", value, now, ok, want, floor)
		}
	})
}

// TestWritesItemsReadsTheVerbAfterAnyWhiteSpace: a PartiQL INSERT or
// UPDATE is a write whatever white space surrounds its verb, as in a
// multi-line statement; a SELECT, a DELETE and an empty statement are not.
func TestWritesItemsReadsTheVerbAfterAnyWhiteSpace(t *testing.T) {
	for st, want := range map[string]bool{
		`UPDATE "Locks" SET exp = 1`:    true,
		"update\n\"Locks\" SET exp = 1": true,
		"INSERT\tINTO \"Locks\" VALUE":  true,
		"\n  insert into \"Locks\"":     true,
		`SELECT * FROM "Locks"`:         false,
		`DELETE FROM "Locks"`:           false,
		"UPDATER":                       false,
		"  ":                            false,
	} {
		if got := writesItems([]string{st}); got != want {
			t.Errorf("writesItems(%q) = %v, want %v", st, got, want)
		}
	}
}

// TestIndexThroughputFollowsUpdates: an UpdateTable's index updates
// record each global secondary index's throughput, stamping an increase
// or a decrease at the update's instant, and a deleted index's record
// goes; switching to PAY_PER_REQUEST drops every record.
func TestIndexThroughputFollowsUpdates(t *testing.T) {
	at := func(sec int64) clock.Instant { return clock.FromWire(time.Unix(sec, 0).UTC()) }
	pt := func(r, w int64) *dynamodbapi.ProvisionedThroughput {
		return &dynamodbapi.ProvisionedThroughput{ReadCapacityUnits: r, WriteCapacityUnits: w}
	}
	tbl := Table{IndexThroughput: map[string]Throughput{"ByOwner": {Reads: 5, Writes: 5}, "Old": {Reads: 1, Writes: 1}}}
	next := tbl.updated(&dynamodbapi.UpdateTableInput{GlobalSecondaryIndexUpdates: []dynamodbapi.GlobalSecondaryIndexUpdate{
		{Update: &dynamodbapi.UpdateGlobalSecondaryIndexAction{IndexName: "ByOwner", ProvisionedThroughput: pt(10, 2)}},
		{Create: &dynamodbapi.CreateGlobalSecondaryIndexAction{IndexName: "New", ProvisionedThroughput: pt(3, 3)}},
		{Delete: &dynamodbapi.DeleteGlobalSecondaryIndexAction{IndexName: "Old"}},
	}}, at(100), nil)
	want := map[string]Throughput{
		"ByOwner": {Reads: 10, Writes: 2, Increased: at(100), Decreased: at(100)},
		"New":     {Reads: 3, Writes: 3},
	}
	if len(next.IndexThroughput) != len(want) {
		t.Fatalf("IndexThroughput = %+v, want %+v", next.IndexThroughput, want)
	}
	for name, w := range want {
		got := next.IndexThroughput[name]
		if got.Reads != w.Reads || got.Writes != w.Writes || got.Increased.Compare(w.Increased) != 0 || got.Decreased.Compare(w.Decreased) != 0 {
			t.Errorf("IndexThroughput[%s] = %+v, want %+v", name, got, w)
		}
	}
	if _, kept := tbl.IndexThroughput["Old"]; !kept || len(tbl.IndexThroughput) != 2 {
		t.Errorf("the update changed the record it started from: %+v", tbl.IndexThroughput)
	}
	onDemand := next.updated(&dynamodbapi.UpdateTableInput{BillingMode: new(dynamodbapi.BillingModePayPerRequest)}, at(200), nil)
	if onDemand.IndexThroughput != nil || onDemand.Throughput != nil {
		t.Errorf("throughput after switching to PAY_PER_REQUEST = %+v, %+v; want none", onDemand.Throughput, onDemand.IndexThroughput)
	}
}

// TestTableKeysRoundTrip: a timer id or section path reads back as the
// table it names.
func TestTableKeysRoundTrip(t *testing.T) {
	for _, path := range []string{"111111111111/us-east-1/Locks", "000000000000/eu-west-1/a.b-c_d"} {
		k, err := parseTableKey(path)
		if err != nil || k.path() != path {
			t.Errorf("parseTableKey(%q) = %+v, %v", path, k, err)
		}
	}
	for _, path := range []string{"Locks", "111111111111/us-east-1/", "111111111111/Locks", "bad/us-east-1/Locks"} {
		if k, err := parseTableKey(path); err == nil {
			t.Errorf("parseTableKey(%q) = %+v, want an error", path, k)
		}
	}
}
