package ids

import (
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// Access key layout (iam-sts-kms.md 2.2): after the four-letter prefix, 16
// base32 characters encode 10 bytes. The first six bytes are a big-endian
// 48-bit window whose top bit is set (a new-generation key), whose next 40
// bits hold the account number shifted left by 7, and whose low 7 bits are
// random; the last four bytes are random.
const (
	accessKeyLen    = 20
	accessKeyBody   = 10 // decoded bytes after the prefix
	windowBytes     = 6  // the 48-bit window at the start of the body
	accountMask     = 0x7fffffffff80
	accountShift    = 7
	generationBit   = 1 << 47
	maxAccount      = 999_999_999_999
	accountDigits   = 12
	longTermPrefix  = "AKIA"
	temporaryPrefix = "ASIA"
)

// AccessKeyID mints a long-term access key id (AKIA) that decodes to
// account, which must be 12 ASCII digits.
func (s *Source) AccessKeyID(account string) (AccessKeyID, error) {
	return s.accessKey(longTermPrefix, account)
}

// TemporaryAccessKeyID mints a temporary (STS) access key id (ASIA) that
// decodes to account, which must be 12 ASCII digits.
func (s *Source) TemporaryAccessKeyID(account string) (AccessKeyID, error) {
	return s.accessKey(temporaryPrefix, account)
}

// DecodeAccount returns the 12-digit account encoded in an AKIA or ASIA
// access key id, as AWS's GetAccessKeyInfo does. It reports false for keys
// that are not 20 characters, carry another prefix, are not valid base32
// (AWS documentation examples such as AKIAI44QH8DHBEXAMPLE are not) or
// encode a number with more than 12 digits.
func DecodeAccount(key string) (string, bool) {
	_, window, ok := decodeKey(key)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%0*d", accountDigits, window&accountMask>>accountShift), true
}

// CompareAccessKeys orders access key ids by everything but the account
// they encode, then by the whole id. Keys drawn from one seeded stream
// under different accounts therefore sort alike, so state sorted by key
// lines up between runs under different accounts (snapshot diff
// --normalize). A key DecodeAccount refuses sorts by the whole id.
func CompareAccessKeys(a, b AccessKeyID) int {
	if c := strings.Compare(withoutAccount(a), withoutAccount(b)); c != 0 {
		return c
	}
	return strings.Compare(string(a), string(b))
}

// withoutAccount is key with the account bits of its window cleared, or key
// itself when DecodeAccount refuses it.
func withoutAccount(key AccessKeyID) string {
	body, window, ok := decodeKey(string(key))
	if !ok {
		return string(key)
	}
	var w [8]byte
	binary.BigEndian.PutUint64(w[:], (window&^accountMask)<<16)
	copy(body, w[:windowBytes])
	return string(key[:4]) + base32.StdEncoding.EncodeToString(body)
}

// decodeKey returns the decoded body of an AKIA or ASIA key and its 48-bit
// window, reporting false for any key DecodeAccount refuses.
func decodeKey(key string) (body []byte, window uint64, ok bool) {
	if len(key) != accessKeyLen || (key[:4] != longTermPrefix && key[:4] != temporaryPrefix) {
		return nil, 0, false
	}
	body, err := base32.StdEncoding.DecodeString(key[4:])
	if err != nil || len(body) != accessKeyBody {
		return nil, 0, false
	}
	var w [8]byte
	copy(w[:windowBytes], body)
	window = binary.BigEndian.Uint64(w[:]) >> 16
	if window&accountMask>>accountShift > maxAccount {
		return nil, 0, false
	}
	return body, window, true
}

func (s *Source) accessKey(prefix, account string) (AccessKeyID, error) {
	n, ok := parseAccount(account)
	if !ok {
		return "", &InvalidAccountError{Account: account}
	}
	body := s.Draw(KindAccessKeyID, accessKeyBody)
	random := uint64(body[windowBytes-1]) &^ accountMask // the low 7 bits of the window stay random
	var w [8]byte
	binary.BigEndian.PutUint64(w[:], (generationBit|n<<accountShift|random)<<16)
	copy(body, w[:windowBytes])
	return AccessKeyID(prefix + base32.StdEncoding.EncodeToString(body)), nil
}

func parseAccount(account string) (uint64, bool) {
	if len(account) != accountDigits {
		return 0, false
	}
	for _, c := range []byte(account) {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(account, 10, 64)
	return n, err == nil
}
