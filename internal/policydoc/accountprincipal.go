package policydoc

import (
	"encoding/json"
	"errors"
	"strings"
)

// RootAccountPrincipals returns a resource policy's text with every bare
// account id a statement's Principal names under AWS written as that
// account's root ARN in partition, arn:<partition>:iam::<account>:root, as
// SQS, SNS, S3 and KMS store it (iam-evaluation.md 5.4, switch
// iam.resource-policy.account-id-principal). The two name the same
// principal, so evaluation is unchanged. Everything else, layout and key
// order included, is kept byte for byte, since these services otherwise
// echo the policy as sent. NotPrincipal is left as sent: no recording shows
// a service converting it. Text that is not JSON is returned unchanged.
func RootAccountPrincipals(text, partition string) string {
	w := principalWalker{text: text, dec: json.NewDecoder(strings.NewReader(text))}
	if w.value(inDocument) != nil || len(w.accounts) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, a := range w.accounts {
		b.WriteString(text[last:a.start])
		b.WriteString(`"arn:` + partition + ":iam::" + a.id + `:root"`)
		last = a.end
	}
	b.WriteString(text[last:])
	return b.String()
}

// principalRole is where in a policy document a JSON value sits, as far as
// RootAccountPrincipals cares.
type principalRole uint8

const (
	inOther principalRole = iota
	inDocument
	inStatements
	inStatement
	inPrincipal
	inAWSPrincipals
)

// errObjectKey is an object key token that is not a string, which
// encoding/json never yields.
var errObjectKey = errors.New("policydoc: object key is not a string")

// accountSpan is a bare account id's JSON string, quotes included, at
// text[start:end].
type accountSpan struct {
	start, end int
	id         string
}

// principalWalker walks a document's tokens, collecting the account ids
// named as AWS principals.
type principalWalker struct {
	text     string
	dec      *json.Decoder
	accounts []accountSpan
}

func (w *principalWalker) value(r principalRole) error {
	before := int(w.dec.InputOffset())
	tok, err := w.dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			if r == inStatements {
				r = inStatement
			}
			return w.object(r)
		}
		// '[': only a statement list and an AWS principal list matter.
		switch r {
		case inStatements:
			return w.array(inStatement)
		case inAWSPrincipals:
			return w.array(inAWSPrincipals)
		case inOther, inDocument, inStatement, inPrincipal:
		}
		return w.array(inOther)
	case string:
		if r == inAWSPrincipals && isAccountID(t) {
			// Between the previous token and the string lie only
			// whitespace, ':' and ',', so its first quote opens it.
			end := int(w.dec.InputOffset())
			start := before + strings.IndexByte(w.text[before:end], '"')
			w.accounts = append(w.accounts, accountSpan{start: start, end: end, id: t})
		}
	}
	return nil
}

func (w *principalWalker) object(r principalRole) error {
	for w.dec.More() {
		tok, err := w.dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			// The decoder yields every object key as a string.
			return errObjectKey
		}
		child := inOther
		switch {
		case r == inDocument && key == "Statement":
			child = inStatements
		case r == inStatement && key == "Principal":
			child = inPrincipal
		case r == inPrincipal && key == "AWS":
			child = inAWSPrincipals
		}
		if err := w.value(child); err != nil {
			return err
		}
	}
	_, err := w.dec.Token()
	return err
}

func (w *principalWalker) array(elem principalRole) error {
	for w.dec.More() {
		if err := w.value(elem); err != nil {
			return err
		}
	}
	_, err := w.dec.Token()
	return err
}

// isAccountID reports whether s is twelve ASCII digits.
func isAccountID(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
