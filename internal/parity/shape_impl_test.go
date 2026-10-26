package parity

import (
	"errors"
	"strings"
	"testing"
)

// An ARN may leave its region empty only where the recorded side's does,
// as IAM's ARNs do; a regional recording binds a regional value.
func TestCheckBindRegionlessARNs(t *testing.T) {
	t.Parallel()
	role := ARNShape{Service: "iam", Resource: "role"}
	if err := CheckBind(role, recordedRole, "arn:aws:iam:us-east-2:123456789012:role/usr-rec-20261014-trust", shapeAccount, shapeRegion); err != nil {
		t.Errorf("CheckBind(regionless aws, scenario-region ours): %v; want nil", err)
	}
	if err := CheckBind(role, recordedRole, "arn:aws:iam:eu-west-1:123456789012:role/usr-rec-20261014-trust", shapeAccount, shapeRegion); err == nil {
		t.Error("CheckBind(regionless aws, another region ours) = nil; want an error")
	}
	key := ARNShape{Service: "kms", Resource: "key", Name: UUIDShape{}}
	err := CheckBind(key, recordedKey, "arn:aws:kms::123456789012:key/"+mintedKeyID, shapeAccount, shapeRegion)
	if se, ok := errors.AsType[*ShapeError](err); !ok || se.Side != "understudy" {
		t.Errorf("CheckBind(regional aws, regionless ours) error = %v; want the understudy side refused", err)
	}
}

// A shape whose name is free still binds two different names, and an ID's
// alphabet is checked after its prefix only.
func TestCheckBindFreesWhatTheShapeNames(t *testing.T) {
	t.Parallel()
	key := ARNShape{Service: "kms", Resource: "key", Name: UUIDShape{}}
	if err := CheckBind(key, recordedKey, mintedKey, shapeAccount, shapeRegion); err != nil {
		t.Errorf("CheckBind(two key ARNs): %v; want nil", err)
	}
	prefixed := IDShape{Alphabet: Digits, Min: 5, Max: 5, Prefix: "id-"}
	if err := CheckBind(prefixed, "id-12", "id-34", shapeAccount, shapeRegion); err != nil {
		t.Errorf("CheckBind(prefixed digits): %v; want nil", err)
	}
}

// A character outside the alphabet is quoted whole, not as the first byte
// of its encoding.
func TestCheckBindQuotesAWholeCharacter(t *testing.T) {
	t.Parallel()
	err := CheckBind(IDShape{Alphabet: Digits, Min: 1, Max: 10}, "12é3", "123", shapeAccount, shapeRegion)
	if se, ok := errors.AsType[*ShapeError](err); !ok || !strings.Contains(se.Reason, `holds 'é'`) {
		t.Errorf("CheckBind(non-ASCII outside the alphabet) error = %v; want the reason to quote 'é'", err)
	}
}
