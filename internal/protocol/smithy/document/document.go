// Package document holds the Smithy document value: protocol-agnostic,
// JSON-like data carried by document members and default trait values.
package document

import (
	"fmt"
	"strconv"
)

// Value is a document value.
//
//sumtype:decl
type Value interface{ isValue() }

// Null is the null value.
type Null struct{}

// Bool is a boolean.
type Bool bool

// Number is a number kept as its decimal text so no codec loses precision.
// ParseNumber is the validating constructor.
type Number string

// String is a string.
type String string

// List is an ordered sequence of values.
type List []Value

// Map is an object. Entries keep wire order; decoders keep the last of
// duplicate keys, so keys are unique.
type Map []Entry

// Entry is one key and value of a Map.
type Entry struct {
	Key   string
	Value Value
}

// NumberError reports text that is not a decimal number.
type NumberError struct {
	Text string
}

func (e *NumberError) Error() string {
	return fmt.Sprintf("%q is not a number: a document number is decimal text such as -12, 0.5 or 1e10", e.Text)
}

// ParseNumber accepts the JSON number grammar: an optional minus, an integer
// part without leading zeros, an optional fraction and an optional exponent.
func ParseNumber(s string) (Number, error) {
	if !validNumber(s) {
		return "", &NumberError{Text: s}
	}
	return Number(s), nil
}

// Int64 converts n when it is an integer in range.
func (n Number) Int64() (int64, error) {
	return strconv.ParseInt(string(n), 10, 64)
}

// Float64 converts n to the nearest float64.
func (n Number) Float64() (float64, error) {
	return strconv.ParseFloat(string(n), 64)
}

func (Null) isValue()   {}
func (Bool) isValue()   {}
func (Number) isValue() {}
func (String) isValue() {}
func (List) isValue()   {}
func (Map) isValue()    {}

func validNumber(s string) bool {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	if i < len(s) && s[i] == '0' {
		i++
	} else {
		end := skipDigits(s, i)
		if end == i {
			return false
		}
		i = end
	}
	if i < len(s) && s[i] == '.' {
		end := skipDigits(s, i+1)
		if end == i+1 {
			return false
		}
		i = end
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		end := skipDigits(s, i)
		if end == i {
			return false
		}
		i = end
	}
	return i == len(s)
}

// skipDigits returns the index of the first non-digit at or after i.
func skipDigits(s string, i int) int {
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}
