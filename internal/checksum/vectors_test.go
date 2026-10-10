package checksum_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/checksum"
)

// pattern returns n bytes where byte i is i mod 251, the input the research
// vectors call patN (s3-checksum-residuals-and-record-backlog.md 1.4).
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// failer is what the helpers need from *testing.T and *rapid.T.
type failer interface {
	Helper()
	Fatalf(format string, args ...any)
}

func sumOf(t failer, alg checksum.Algorithm, data []byte) checksum.Sum {
	t.Helper()
	h, err := checksum.New(alg)
	if err != nil {
		t.Fatalf("New(%v): %v", alg, err)
	}
	if _, err := h.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return h.Checksum()
}

// TestPublishedVectors checks the wire value (base64 of the big-endian digest)
// for every vector in go-toolchain.md 7.3, sigv4-checksums-endpoints.md 4.2,
// s3.md 6.1 and s3-checksum-residuals-and-record-backlog.md 1.4.1 and 1.4.3.
func TestPublishedVectors(t *testing.T) {
	t.Parallel()
	const hello = "Hello, world!"
	cases := []struct {
		alg   checksum.Algorithm
		input string
		want  string
	}{
		{checksum.CRC32, "", "AAAAAA=="},
		{checksum.CRC32, "123456789", "y/Q5Jg=="},
		{checksum.CRC32, hello, "6+bG5g=="},
		{checksum.CRC32, strings.Repeat("a", 1000), "mjjaAw=="},
		{checksum.CRC32C, "", "AAAAAA=="},
		{checksum.CRC32C, "123456789", "4waSgw=="},
		{checksum.CRC32C, hello, "yKEG5Q=="},
		{checksum.CRC32C, strings.Repeat("a", 66560), "sOO8/Q=="},
		{checksum.CRC64NVME, "", "AAAAAAAAAAA="},
		{checksum.CRC64NVME, "123456789", "rosUhgp5mIg="},
		{checksum.CRC64NVME, hello, "n3hVaQAaPTQ="},
		{checksum.CRC64NVME, "hi\n", "JYwwIoi0PuE="},
		{checksum.CRC64NVME, strings.Repeat("a", 1000), "6rMjKymX9eU="},
		{checksum.SHA1, "", "2jmj7l5rSw0yVb/vlWAYkK/YBwk="},
		{checksum.SHA1, "123456789", "98O8HYCOBHMq32eZZczDTKeuNEE="},
		{checksum.SHA1, hello, "lDpwLQbzRZmu4fjajvn3KWAx1pk="},
		{checksum.SHA256, "", "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="},
		{checksum.SHA256, "123456789", "FeKw08M4keuw8e9gnsQZQgwg4yDOlMZfvIwzEkSOsiU="},
		{checksum.SHA256, hello, "MV9b23bQeMQ7isAGTkoBZGErH853yGk0W/yUx1iU7dM="},
		{checksum.MD5, "", "1B2M2Y8AsgTpgAmY7PhCfg=="},
		{checksum.MD5, "123456789", "JfnnlDI7RTiF9RgfG2JNCw=="},
		{checksum.MD5, hello, "bNNVbesNpUvKBgtMOUeYOQ=="},
		{checksum.MD5, "hello", "XUFAKrxLKna5cZ2REBfFkg=="},
		{checksum.MD5, string(pattern(1 << 20)), "jyk6L2wZs0UVL3pJu0xkPA=="},
		{checksum.SHA512, "hello", "m3HSJL1i83hdltRq0+o9czGb+8KJDKra4t/3JRlnPKcjI8PZm6XBHXx6zG4UuMXaDEZjR1wuXDre9G9zvN7AQw=="},
		{checksum.SHA512, string(pattern(1 << 20)), "Z9rVae78mGo7JCT1UW1aAoS7U9e1LXX17YgaaDCpV2XMyCvEh1L7aTQiV58R3JpABWHsGIWvnu73A9u9MS1P0A=="},
		{checksum.XXHASH64, "", "70bbN1HY6Zk="},
		{checksum.XXHASH3, "", "LQaABTjTlMI="},
		{checksum.XXHASH128, "", "maoG0wFHmNhgAcMkRo1Jfw=="},
		{checksum.XXHASH64, "hello", "JseCfYifbaM="},
		{checksum.XXHASH3, "hello", "lVXoVVxi3P0="},
		{checksum.XXHASH128, "hello", "tenBrQcbPn/Hec+qXlI4GA=="},
		{checksum.XXHASH64, "123456789", "jLhB20DmroM="},
		{checksum.XXHASH3, "123456789", "ctyxi2ehff8="},
		{checksum.XXHASH128, "123456789", "MxGUd+3l3NXpcWQnaB1YYA=="},
		{checksum.XXHASH64, string(pattern(1 << 20)), "iawDmcRGSjE="},
		{checksum.XXHASH3, string(pattern(1 << 20)), "bg16w2uMEP8="},
		{checksum.XXHASH128, string(pattern(1 << 20)), "U3ONmAmMq7puDXrDa4wQ/w=="},
		{checksum.XXHASH64, string(pattern(5 << 20)), "nt7qU/z8nB4="},
		{checksum.XXHASH3, string(pattern(5 << 20)), "f7lM6WcDDeA="},
		{checksum.XXHASH128, string(pattern(5 << 20)), "N+DwsSPHJSV/uUzpZwMN4A=="},
		{checksum.SHA512, string(pattern(5 << 20)), "UUcj5aGVOfG4H6SovYePw1sFP9roSxo6w6etwJlWkni7M72Zg5eKqAAgQv+F3SB1Yet8+jJEY8j2ZtKv22VTPw=="},
		{checksum.MD5, string(pattern(5 << 20)), "TChkDcjfGTOq6hkhANUK4A=="},
	}
	for _, c := range cases {
		got := sumOf(t, c.alg, []byte(c.input)).String()
		if got != c.want {
			t.Errorf("%v over %d bytes = %s, want %s", c.alg, len(c.input), got, c.want)
		}
	}
}

// TestXXHashLengthClasses covers every branch of the XXH3 length dispatch and
// the XXH64 stripe loop with the hex vectors of
// s3-checksum-residuals-and-record-backlog.md 1.4.2, plus the CRT upstream test
// input "Hello world".
func TestXXHashLengthClasses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input                 []byte
		xxh64, xxh3, xxh3x128 string
	}{
		{[]byte("Hello world"), "c500b0c912b376d8", "b6acb9d84a38ff74", "7351f89812f97382b91d05b31e04dd7f"},
		{pattern(1), "e934a84adb052768", "c44bdff4074eecdb", "a6cd5e9392000f6ac44bdff4074eecdb"},
		{pattern(3), "e5c7bb4533bc65dd", "5f4299fc161c9cbb", "e3b55f57945a17cf5f4299fc161c9cbb"},
		{pattern(4), "ffced8604453cc1e", "60dab036a58211f2", "eb70bf5fc779e9e6a6111d53e80a3db5"},
		{pattern(8), "884a173614b81b8d", "3a1c2d7c85af88f8", "e1e4432a62217fe4cfd50c61c8bb98c1"},
		{pattern(9), "67d85784a7c78c5b", "e9612598145bb9dc", "16c769d83e4aebce907931979dca3746"},
		{pattern(16), "44b6ef2fb84169f7", "8355e3a6f61770db", "72950631827607e2842812cc870dcae2"},
		{pattern(17), "5603e60c527599b6", "9ef341a99de37328", "685bc458b37d057fc06e233df7729217"},
		{pattern(128), "7a7fe14647b9ab92", "85c6174c7ff4c46b", "14792fc3af88dc6c05321a0b64d67b41"},
		{pattern(129), "0ba25dfd6e891fcf", "ec7642b431ba3e5a", "dd5e74ac6b45f54ebc30b63382b09a3b"},
		{pattern(240), "012947f0da6a27b1", "375a384d957fe865", "65b5be86da5540e7c92b68e16f83bbb6"},
		{pattern(241), "8d643f23bf2808e1", "02e8cd95421c6d02", "1da1cb61bcb8a2a102e8cd95421c6d02"},
		{pattern(1023), "d66738f081c25cf4", "d3d91d80ac495685", "4325711b0ed4d742d3d91d80ac495685"},
		{pattern(1024), "138e26c65048ce29", "e5d78bafa45b2aa5", "d0ac1f7b93bf57b9e5d78bafa45b2aa5"},
		{pattern(1025), "cfd73aedd2d6a39d", "e95c42288f28186e", "2882ebca04ec915ce95c42288f28186e"},
		{pattern(4096), "122a8c8d994ad3ec", "7135ffa504f1bc71", "e12cd72144990fe57135ffa504f1bc71"},
	}
	for _, c := range cases {
		for alg, want := range map[checksum.Algorithm]string{
			checksum.XXHASH64:  c.xxh64,
			checksum.XXHASH3:   c.xxh3,
			checksum.XXHASH128: c.xxh3x128,
		} {
			got := hex.EncodeToString(sumOf(t, alg, c.input).Bytes())
			if got != want {
				t.Errorf("%v over %d bytes = %s, want %s", alg, len(c.input), got, want)
			}
		}
	}
}
