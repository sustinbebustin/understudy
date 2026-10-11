package protocoltest_test

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/protocoltest"
)

var (
	timeType   = reflect.TypeFor[time.Time]()
	bodyType   = reflect.TypeFor[smithy.Body]()
	numberType = reflect.TypeFor[document.Number]()
)

// diffInput compares a decoded input with the case's params: exported
// fields only (the generated required-member tracking is not part of the
// value), nil and empty collections told apart, timestamps by instant, NaN
// equal to NaN, document numbers by value, and streams by their contents,
// an absent stream reading as an empty one.
func diffInput(want, got any) error {
	return diffValue("input", reflect.ValueOf(want), reflect.ValueOf(got))
}

func diffValue(path string, want, got reflect.Value) error {
	mismatch := func() error {
		return &protocoltest.MismatchError{Where: path, Want: describe(want), Got: describe(got)}
	}
	if want.IsValid() != got.IsValid() || want.IsValid() && want.Type() != got.Type() {
		return mismatch()
	}
	if !want.IsValid() {
		return nil
	}
	if want.Type() == bodyType {
		return diffBody(path, want, got)
	}
	switch k := want.Kind(); {
	case k == reflect.Interface || k == reflect.Pointer:
		if want.IsNil() || got.IsNil() {
			if want.IsNil() != got.IsNil() {
				return mismatch()
			}
			return nil
		}
		return diffValue(path, want.Elem(), got.Elem())
	case k == reflect.Struct:
		return diffStruct(path, want, got, mismatch)
	case k == reflect.Slice:
		return diffSlice(path, want, got, mismatch)
	case k == reflect.Map:
		return diffMap(path, want, got, mismatch)
	case k == reflect.Float32 || k == reflect.Float64:
		if w, g := want.Float(), got.Float(); w != g && !(math.IsNaN(w) && math.IsNaN(g)) {
			return mismatch()
		}
		return nil
	case want.Type() == numberType:
		if !sameNumber(want.String(), got.String()) {
			return mismatch()
		}
		return nil
	case !want.Equal(got):
		return mismatch()
	default:
		return nil
	}
}

func diffStruct(path string, want, got reflect.Value, mismatch func() error) error {
	if want.Type() == timeType {
		w, wok := reflect.TypeAssert[time.Time](want)
		g, gok := reflect.TypeAssert[time.Time](got)
		if !wok || !gok || !w.Equal(g) {
			return mismatch()
		}
		return nil
	}
	for i := range want.NumField() {
		f := want.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		if err := diffValue(path+"."+f.Name, want.Field(i), got.Field(i)); err != nil {
			return err
		}
	}
	return nil
}

func diffSlice(path string, want, got reflect.Value, mismatch func() error) error {
	if want.IsNil() != got.IsNil() || want.Len() != got.Len() {
		return mismatch()
	}
	if want.Type().Elem().Kind() == reflect.Uint8 {
		if !bytes.Equal(want.Bytes(), got.Bytes()) {
			return mismatch()
		}
		return nil
	}
	for i := range want.Len() {
		if err := diffValue(fmt.Sprintf("%s[%d]", path, i), want.Index(i), got.Index(i)); err != nil {
			return err
		}
	}
	return nil
}

func diffMap(path string, want, got reflect.Value, mismatch func() error) error {
	if want.IsNil() != got.IsNil() || want.Len() != got.Len() {
		return mismatch()
	}
	for _, key := range want.MapKeys() {
		at := fmt.Sprintf("%s[%v]", path, key)
		g := got.MapIndex(key)
		if !g.IsValid() {
			return &protocoltest.MismatchError{Where: at, Want: describe(want.MapIndex(key)), Got: "no entry"}
		}
		if err := diffValue(at, want.MapIndex(key), g); err != nil {
			return err
		}
	}
	return nil
}

// diffBody compares two streams by their bytes; an absent stream is an
// empty one, since a request without a body carries no stream to tell
// from an empty one.
func diffBody(path string, want, got reflect.Value) error {
	w, err := readBody(want)
	if err != nil {
		return err
	}
	g, err := readBody(got)
	if err != nil {
		return &protocoltest.MismatchError{Where: path, Want: fmt.Sprintf("%q", w), Got: "an unreadable stream: " + err.Error()}
	}
	if !bytes.Equal(w, g) {
		return &protocoltest.MismatchError{Where: path, Want: fmt.Sprintf("%q", w), Got: fmt.Sprintf("%q", g)}
	}
	return nil
}

func readBody(v reflect.Value) ([]byte, error) {
	b, ok := reflect.TypeAssert[smithy.Body](v)
	if !ok || b == nil {
		return nil, nil
	}
	rc, err := b.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// sameNumber compares two document numbers by value, so 1, 1.0 and 1e0
// are equal.
func sameNumber(a, b string) bool {
	x, _, errA := big.ParseFloat(a, 10, 256, big.ToNearestEven)
	y, _, errB := big.ParseFloat(b, 10, 256, big.ToNearestEven)
	return a == b || errA == nil && errB == nil && x.Cmp(y) == 0
}

func describe(v reflect.Value) string {
	if !v.IsValid() {
		return "nothing"
	}
	k := v.Kind()
	if (k == reflect.Pointer || k == reflect.Interface || k == reflect.Slice || k == reflect.Map) && v.IsNil() {
		return "nil " + v.Type().String()
	}
	if k == reflect.Pointer {
		return "&" + describe(v.Elem())
	}
	return fmt.Sprintf("%#v", v.Interface())
}
