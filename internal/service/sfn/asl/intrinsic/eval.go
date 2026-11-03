package intrinsic

import (
	"crypto/md5"  //nolint:gosec // States.Hash offers MD5 (stepfunctions-asl.md 5.2): a digest AWS computes, not a security choice
	"crypto/sha1" //nolint:gosec // States.Hash offers SHA-1 (stepfunctions-asl.md 5.2): a digest AWS computes, not a security choice
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"math"
	"math/big"
	mathrand "math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// Limits of stepfunctions-asl.md 5.2 (aws-doc dg/intrinsic-functions).
const (
	// maxRangeItems is the most items States.ArrayRange may return.
	maxRangeItems = 1000
	// maxDataChars is the longest string Base64Encode, Base64Decode and
	// Hash accept, in characters.
	maxDataChars = 10000
)

// Eval evaluates c. Path arguments read input, or context for "$$" paths;
// States.UUID and unseeded States.MathRandom draw from src. A failing
// function is a *FunctionError; a missing path argument is the
// *jsonpath.MissingError of that path.
func (c Call) Eval(input, context document.Value, src Source) (document.Value, error) {
	e := evaluator{input: input, context: context, src: src}
	return e.call(c)
}

// evaluator is one evaluation's inputs.
type evaluator struct {
	input, context document.Value
	src            Source
}

func (e evaluator) call(c Call) (document.Value, error) {
	if c.function == "States.Format" {
		return e.format(c)
	}
	args := make([]document.Value, 0, len(c.args))
	for _, a := range c.args {
		v, err := e.arg(a)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	f := fn{name: c.function, args: args}
	switch c.function {
	case "States.StringToJson":
		return f.stringToJSON()
	case "States.JsonToString":
		// Numbers as Java writes them (ASL-06, recorded 2026-11-03).
		return document.String(compactJSON(javaNumbers(args[0]))), nil
	case "States.Array":
		return document.List(args), nil
	case "States.ArrayPartition":
		return f.arrayPartition()
	case "States.ArrayContains":
		return f.arrayContains()
	case "States.ArrayRange":
		return f.arrayRange()
	case "States.ArrayGetItem":
		return f.arrayGetItem()
	case "States.ArrayLength":
		arr, err := f.array()
		if err != nil {
			return nil, err
		}
		return intNumber(int64(len(arr))), nil
	case "States.ArrayUnique":
		return f.arrayUnique()
	case "States.Base64Encode":
		return f.base64Encode()
	case "States.Base64Decode":
		return f.base64Decode()
	case "States.Hash":
		return f.hash()
	case "States.JsonMerge":
		return f.jsonMerge()
	case "States.MathRandom":
		return f.mathRandom(e.src)
	case "States.MathAdd":
		return f.mathAdd()
	case "States.StringSplit":
		return f.stringSplit()
	case "States.UUID":
		return document.String(e.src.UUID()), nil
	}
	// Parse admits only the functions above.
	return nil, f.invalid()
}

// arg is one argument's value.
func (e evaluator) arg(a arg) (document.Value, error) {
	switch a := a.(type) {
	case stringArg:
		return document.String(a.value), nil
	case literalArg:
		return a.value, nil
	case pathArg:
		return a.path.Eval(e.input, e.context)
	case callArg:
		return e.call(a.call)
	}
	return document.Null{}, nil
}

// fn is a function applied to its evaluated arguments.
type fn struct {
	name string
	args []document.Value
}

// fail is the function failing with AWS's detail text.
func (f fn) fail(detail string) error {
	return &FunctionError{Function: f.name, Detail: detail}
}

// invalid is the function failing with AWS's generic detail, "Invalid
// arguments in <function>": recorded for a negative ArrayGetItem index, an
// ArrayPartition chunk of 0, an ArrayRange of over 1000 items or step 0, an
// unknown Hash algorithm and a MathAdd overflow (ASL-05, recorded
// 2026-11-03), and inferred for every other argument a function refuses.
func (f fn) invalid() error {
	return f.fail("Invalid arguments in " + f.name)
}

// array is the first argument, which every Array function takes as its
// array.
func (f fn) array() (document.List, error) {
	if l, ok := f.args[0].(document.List); ok {
		return l, nil
	}
	return nil, f.invalid()
}

func (f fn) object(i int) (document.Map, error) {
	if m, ok := f.args[i].(document.Map); ok {
		return m, nil
	}
	return nil, f.invalid()
}

func (f fn) str(i int) (string, error) {
	if s, ok := f.args[i].(document.String); ok {
		return string(s), nil
	}
	return "", f.invalid()
}

// data is a string argument of at most maxDataChars characters.
func (f fn) data(i int) (string, error) {
	s, err := f.str(i)
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(s) > maxDataChars {
		return "", f.invalid()
	}
	return s, nil
}

// rounded is a number argument rounded half up, as Java Math.round does
// (stepfunctions-asl.md 5.3: recorded for MathAdd; applying it to
// ArrayPartition, ArrayRange and MathRandom is inferred). It must fit an
// int64.
func (f fn) rounded(i int) (int64, error) {
	n, ok := f.args[i].(document.Number)
	if !ok {
		return 0, f.invalid()
	}
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return 0, f.invalid()
	}
	// floor(x + 1/2): big.Int.Div is Euclidean, so with the positive
	// denominator a Rat keeps it is floor division.
	r.Add(r, big.NewRat(1, 2))
	q := new(big.Int).Div(r.Num(), r.Denom())
	if !q.IsInt64() {
		return 0, f.invalid()
	}
	return q.Int64(), nil
}

// integer is a number argument that must be an integer.
func (f fn) integer(i int) (int64, error) {
	n, ok := f.args[i].(document.Number)
	if !ok {
		return 0, f.invalid()
	}
	r, ok := new(big.Rat).SetString(string(n))
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		return 0, f.invalid()
	}
	return r.Num().Int64(), nil
}

func intNumber(n int64) document.Number { return document.Number(strconv.FormatInt(n, 10)) }

// format is States.Format: each "{}" of the format string takes the next
// argument; an escaped \{ or \} is the character itself
// (stepfunctions-asl.md 5.1, 5.2).
func (e evaluator) format(c Call) (document.Value, error) {
	f := fn{name: c.function}
	// A literal format string keeps its escapes; one read from a path or a
	// call has none.
	pattern, escaped := "", false
	if lit, ok := c.args[0].(stringArg); ok {
		pattern, escaped = lit.raw, true
	} else {
		v, err := e.arg(c.args[0])
		if err != nil {
			return nil, err
		}
		s, ok := v.(document.String)
		if !ok {
			return nil, f.invalid()
		}
		pattern = string(s)
	}
	values := make([]string, 0, len(c.args)-1)
	for _, a := range c.args[1:] {
		v, err := e.arg(a)
		if err != nil {
			return nil, err
		}
		values = append(values, javaString(v))
	}
	var b strings.Builder
	used := 0
	for i := 0; i < len(pattern); i++ {
		switch {
		case escaped && pattern[i] == '\\' && i+1 < len(pattern):
			i++
			b.WriteByte(pattern[i])
		case pattern[i] == '{' && i+1 < len(pattern) && pattern[i+1] == '}':
			if used == len(values) {
				return nil, f.fail(errFormatCount)
			}
			b.WriteString(values[used])
			used++
			i++
		default:
			b.WriteByte(pattern[i])
		}
	}
	if used != len(values) {
		return nil, f.fail(errFormatCount)
	}
	return document.String(b.String()), nil
}

// errFormatCount is States.Format's detail when the placeholders and the
// arguments differ in number: recorded for more placeholders than
// arguments (ASL-05, recorded 2026-11-03), inferred for fewer.
const errFormatCount = "Invalid arguments in States.Format: number of arguments do not match the occurrences of {}."

// javaString is how States.Format writes a value: strings unquoted, objects
// as {k=v, ...} and arrays as [a, b], as Java's toString does
// (aws-recorded, localstack@8b9a79f
// v2/intrinsic_functions/test_generic.snapshot.json:L64-L1139). The array
// form is inferred from the object form.
func javaString(v document.Value) string {
	switch v := v.(type) {
	case document.String:
		return string(v)
	case document.Number:
		return string(javaNumber(v))
	case document.Bool:
		return strconv.FormatBool(bool(v))
	case document.Map:
		parts := make([]string, 0, len(v))
		for _, m := range v {
			parts = append(parts, m.Key+"="+javaString(m.Value))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case document.List:
		parts := make([]string, 0, len(v))
		for _, x := range v {
			parts = append(parts, javaString(x))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case document.Null:
		return "null"
	}
	return "null"
}

// javaNumber is n as Java writes the number Jackson reads: an integer as
// written, however long; a decimal as a double, through Double.toString:
// at least one fraction digit, and scientific notation (1.0E21) outside
// [10^-3, 10^7). Recorded for 1.0, 1e21, -0.0, 0.1 and 10^20 (ASL-06,
// 2026-11-03).
func javaNumber(n document.Number) document.Number {
	if integral(n) {
		return n
	}
	d, err := strconv.ParseFloat(string(n), 64)
	if err != nil {
		// Out of a double's range: Jackson's text is unrecorded.
		return n
	}
	if a := math.Abs(d); a != 0 && (a < 1e-3 || a >= 1e7) {
		mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(d, 'e', -1, 64), "e")
		if !strings.Contains(mantissa, ".") {
			mantissa += ".0"
		}
		// FormatFloat's exponent is signed and at least two digits.
		e, err := strconv.Atoi(exponent)
		if err != nil {
			return n
		}
		return document.Number(mantissa + "E" + strconv.Itoa(e))
	}
	s := strconv.FormatFloat(d, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return document.Number(s)
}

// javaNumbers is v with every number as javaNumber writes it.
func javaNumbers(v document.Value) document.Value {
	switch v := v.(type) {
	case document.Number:
		return javaNumber(v)
	case document.Map:
		out := make(document.Map, 0, len(v))
		for _, e := range v {
			out = append(out, document.Entry{Key: e.Key, Value: javaNumbers(e.Value)})
		}
		return out
	case document.List:
		out := make(document.List, 0, len(v))
		for _, x := range v {
			out = append(out, javaNumbers(x))
		}
		return out
	case document.String, document.Bool, document.Null:
		return v
	}
	return v
}

// compactJSON is v as compact JSON text, member order kept.
func compactJSON(v document.Value) string {
	enc := jsonx.NewEncoder(jsonx.WireOrder)
	enc.Value(v)
	b, err := enc.Bytes()
	if err != nil {
		// Encoder.Value fails only for a nil value, which is null here.
		return "null"
	}
	return string(b)
}

// stringToJSON parses JSON text; blank text is null and "-0" is 0
// (aws-recorded, test_json_manipulation.snapshot.json:L64-L681).
func (f fn) stringToJSON() (document.Value, error) {
	s, err := f.str(0)
	if err != nil {
		return nil, err
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return document.Null{}, nil
	}
	d := jsonx.NewDecoder([]byte(s))
	v, err := d.ReadValue()
	if err == nil {
		err = d.End()
	}
	if err != nil {
		// AWS's text after "caused by: " is the Jackson parser's (ASL-05,
		// recorded 2026-11-03); understudy's parser words its own.
		return nil, f.fail("Cannot convert String to Json in States.StringToJson, caused by: " + err.Error())
	}
	return zeroSign(v), nil
}

// zeroSign rewrites the integer -0 as 0, as Java's integer parse does.
func zeroSign(v document.Value) document.Value {
	switch v := v.(type) {
	case document.Number:
		if v == "-0" {
			return document.Number("0")
		}
	case document.List:
		out := make(document.List, len(v))
		for i, x := range v {
			out[i] = zeroSign(x)
		}
		return out
	case document.Map:
		out := make(document.Map, len(v))
		for i, m := range v {
			out[i] = document.Entry{Key: m.Key, Value: zeroSign(m.Value)}
		}
		return out
	case document.String, document.Bool, document.Null:
	}
	return v
}

// arrayPartition splits an array into chunks of n, the last shorter.
func (f fn) arrayPartition() (document.Value, error) {
	arr, err := f.array()
	if err != nil {
		return nil, err
	}
	n, err := f.rounded(1)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, f.invalid()
	}
	out := document.List{}
	for start := int64(0); start < int64(len(arr)); {
		end := start + min(n, int64(len(arr))-start)
		out = append(out, arr[start:end])
		start = end
	}
	return out, nil
}

func (f fn) arrayContains() (document.Value, error) {
	arr, err := f.array()
	if err != nil {
		return nil, err
	}
	for _, x := range arr {
		// 1 is found in [1.0] (ASL-06, recorded 2026-11-03).
		if deepEqual(x, f.args[1], sameValue) {
			return document.Bool(true), nil
		}
	}
	return document.Bool(false), nil
}

// arrayRange is start to end inclusive by step, at most maxRangeItems
// items. A step that moves away from end gives [] (inferred: AWS's answer
// is unrecorded, ASL-05).
func (f fn) arrayRange() (document.Value, error) {
	var b [3]int64
	for i := range b {
		n, err := f.rounded(i)
		if err != nil {
			return nil, err
		}
		b[i] = n
	}
	start, end, step := b[0], b[1], b[2]
	if step == 0 {
		return nil, f.invalid()
	}
	out := document.List{}
	// (end - start) / step truncated, plus one, is the item count; it is
	// not positive when step moves away from end.
	count := new(big.Int).Sub(big.NewInt(end), big.NewInt(start))
	count.Quo(count, big.NewInt(step))
	count.Add(count, big.NewInt(1))
	if count.Sign() <= 0 {
		return out, nil
	}
	if count.Cmp(big.NewInt(maxRangeItems)) > 0 {
		return nil, f.invalid()
	}
	for i, v := int64(0), start; i < count.Int64(); i, v = i+1, v+step {
		out = append(out, intNumber(v))
	}
	return out, nil
}

func (f fn) arrayGetItem() (document.Value, error) {
	arr, err := f.array()
	if err != nil {
		return nil, err
	}
	i, err := f.integer(1)
	if err != nil {
		return nil, err
	}
	// A negative index fails and one past the end is null (ASL-05,
	// recorded 2026-11-03).
	switch {
	case i < 0:
		return nil, f.invalid()
	case i >= int64(len(arr)):
		return document.Null{}, nil
	}
	return arr[i], nil
}

// arrayUnique keeps the first of equal items, objects compared as member
// sets. An integer never equals a decimal, so 1 and 1.0 both stay (ASL-06,
// recorded 2026-11-03). AWS's output order is a hash set's; understudy
// keeps first-seen order.
func (f fn) arrayUnique() (document.Value, error) {
	arr, err := f.array()
	if err != nil {
		return nil, err
	}
	out := document.List{}
	for _, x := range arr {
		seen := false
		for _, y := range out {
			if deepEqual(x, y, sameNode) {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, x)
		}
	}
	return out, nil
}

// base64Encode is the standard padded encoding of the string's UTF-8
// bytes (aws-recorded, test_encode_decode.snapshot.json:L64-L270).
func (f fn) base64Encode() (document.Value, error) {
	s, err := f.data(0)
	if err != nil {
		return nil, err
	}
	return document.String(base64.StdEncoding.EncodeToString([]byte(s))), nil
}

// base64Decode decodes as Java's MIME decoder does: characters outside the
// Base64 alphabet are skipped, padding is optional and ends the data. Text
// with no Base64 character decodes to "" (ASL-05 06, recorded 2026-11-03,
// for "%%%"); the MIME reading of other text is inferred from it.
func (f fn) base64Decode() (document.Value, error) {
	s, err := f.data(0)
	if err != nil {
		return nil, err
	}
	var kept strings.Builder
	for _, r := range s {
		if r == '=' {
			break
		}
		if strings.ContainsRune(base64Alphabet, r) {
			kept.WriteRune(r)
		}
	}
	b, err := base64.RawStdEncoding.DecodeString(kept.String())
	if err != nil || !utf8.Valid(b) {
		return nil, f.invalid()
	}
	return document.String(b), nil
}

// base64Alphabet is the standard Base64 alphabet, without padding.
const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// hashFor is States.Hash's algorithm named alg (stepfunctions-asl.md 5.2).
func hashFor(alg string) (hash.Hash, bool) {
	switch alg {
	case "MD5":
		return md5.New(), true //nolint:gosec // see the import
	case "SHA-1":
		return sha1.New(), true //nolint:gosec // see the import
	case "SHA-256":
		return sha256.New(), true
	case "SHA-384":
		return sha512.New384(), true
	case "SHA-512":
		return sha512.New(), true
	}
	return nil, false
}

// hash is the lower-case hex digest of the string's UTF-8 bytes.
func (f fn) hash() (document.Value, error) {
	s, err := f.data(0)
	if err != nil {
		return nil, err
	}
	alg, err := f.str(1)
	if err != nil {
		return nil, err
	}
	h, ok := hashFor(alg)
	if !ok {
		return nil, f.invalid()
	}
	h.Write([]byte(s))
	return document.String(hex.EncodeToString(h.Sum(nil))), nil
}

// jsonMerge is the shallow merge AWS supports: the second object's members
// replace the first's, new ones follow (stepfunctions-asl.md 5.2).
func (f fn) jsonMerge() (document.Value, error) {
	a, err := f.object(0)
	if err != nil {
		return nil, err
	}
	b, err := f.object(1)
	if err != nil {
		return nil, err
	}
	deep, ok := f.args[2].(document.Bool)
	if !ok {
		return nil, f.invalid()
	}
	if deep {
		// ASL-05, recorded 2026-11-03.
		return nil, f.fail("Deep merge not supported in States.JsonMerge")
	}
	out := make(document.Map, len(a), len(a)+len(b))
	copy(out, a)
	for _, m := range b {
		replaced := false
		for i := range out {
			if out[i].Key == m.Key {
				out[i].Value = m.Value
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, m)
		}
	}
	return out, nil
}

// mathRandom is an integer in [start, end): unseeded from src, seeded from
// a stream keyed by the seed, so a seed gives the same number every time
// but never AWS's (aws-doc dg/intrinsic-functions; spec StartExecution
// note).
func (f fn) mathRandom(src Source) (document.Value, error) {
	start, err := f.rounded(0)
	if err != nil {
		return nil, err
	}
	end, err := f.rounded(1)
	if err != nil {
		return nil, err
	}
	if start >= end || end-start <= 0 {
		return nil, f.invalid()
	}
	if len(f.args) < 3 {
		return intNumber(src.Random(start, end)), nil
	}
	seed, err := f.rounded(2)
	if err != nil {
		return nil, err
	}
	// The key is the seed's decimal text: deterministic, and no AWS
	// sequence to match (spec StartExecution note).
	var key [32]byte
	copy(key[:], strconv.FormatInt(seed, 10))
	rng := mathrand.New(mathrand.NewChaCha8(key)) //nolint:gosec // a seeded States.MathRandom must repeat for its seed; it is not a secret
	return intNumber(start + rng.Int64N(end-start)), nil
}

// mathAdd adds two numbers, each rounded half up first; arguments and sum
// are 32-bit integers (stepfunctions-asl.md 5.2, 5.3).
func (f fn) mathAdd() (document.Value, error) {
	var sum int64
	for i := range 2 {
		n, err := f.rounded(i)
		if err != nil {
			return nil, err
		}
		if n < math.MinInt32 || n > math.MaxInt32 {
			return nil, f.invalid()
		}
		sum += n
	}
	if sum < math.MinInt32 || sum > math.MaxInt32 {
		return nil, f.invalid()
	}
	return intNumber(sum), nil
}

// stringSplit splits at every character of the delimiter string and drops
// empty pieces (stepfunctions-asl.md 5.2).
func (f fn) stringSplit() (document.Value, error) {
	s, err := f.str(0)
	if err != nil {
		return nil, err
	}
	delims, err := f.str(1)
	if err != nil {
		return nil, err
	}
	out := document.List{}
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(delims, r) }) {
		out = append(out, document.String(p))
	}
	return out, nil
}

// numberEquality says when two numbers are equal; the functions that
// compare values differ in it (ASL-06, recorded 2026-11-03).
type numberEquality func(a, b document.Number) bool

// sameValue compares numbers by value: 1 equals 1.0.
func sameValue(a, b document.Number) bool {
	x, okx := new(big.Rat).SetString(string(a))
	y, oky := new(big.Rat).SetString(string(b))
	return okx && oky && x.Cmp(y) == 0
}

// sameNode compares numbers as Jackson's nodes do: an integer never equals
// a decimal, so 1 and 1.0 differ.
func sameNode(a, b document.Number) bool {
	return integral(a) == integral(b) && sameValue(a, b)
}

// integral reports whether n is written as an integer: no fraction and no
// exponent.
func integral(n document.Number) bool {
	return !strings.ContainsAny(string(n), ".eE")
}

// deepEqual compares JSON values: objects as member sets, arrays in order,
// numbers as the numbers equality says (stepfunctions-asl.md 5.2).
func deepEqual(a, b document.Value, numbers numberEquality) bool {
	switch a := a.(type) {
	case document.Map:
		b, ok := b.(document.Map)
		return ok && objectsEqual(a, b, numbers)
	case document.List:
		b, ok := b.(document.List)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !deepEqual(a[i], b[i], numbers) {
				return false
			}
		}
		return true
	case document.Number:
		b, ok := b.(document.Number)
		return ok && numbers(a, b)
	case document.String:
		b, ok := b.(document.String)
		return ok && a == b
	case document.Bool:
		b, ok := b.(document.Bool)
		return ok && a == b
	case document.Null:
		_, ok := b.(document.Null)
		return ok
	}
	return false
}

// objectsEqual compares objects as member sets.
func objectsEqual(a, b document.Map, numbers numberEquality) bool {
	if len(a) != len(b) {
		return false
	}
	for _, ea := range a {
		i := slices.IndexFunc(b, func(eb document.Entry) bool { return eb.Key == ea.Key })
		if i < 0 || !deepEqual(ea.Value, b[i].Value, numbers) {
			return false
		}
	}
	return true
}
