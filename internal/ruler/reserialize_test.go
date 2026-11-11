package ruler

import (
	"strconv"
	"testing"
)

// TestReserialize: SNS hands Ruler the policy as Jackson writes its tree
// back, compact, with Jackson's string escapes and number forms
// (sns/R09/H5-08-attr-subscribe.json shows 1e309 as "Infinity").
func TestReserialize(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{`{"n":[{"numeric":["=",1e309]}]}`, `{"n":[{"numeric":["=","Infinity"]}]}`},
		{" {\n \"a\" : [ \"x\" , -1e309 ] ,\t\"b\":{ } } ", `{"a":["x","-Infinity"],"b":{}}`},
		{`{"a":["\u0041\/\"\\","\u0001\t\n\b\f\r","é"]}`, `{"a":["A/\"\\","\u0001\t\n\b\f\r","é"]}`},
		{`{"a":[-0,7,123456789012345678901234567890,1.50,1e2,0.0001,1E7,-0.0]}`, `{"a":[0,7,123456789012345678901234567890,1.5,100.0,1.0E-4,1.0E7,-0.0]}`},
		{`{"a":[true,false,null],"a":[1]}`, `{"a":[true,false,null],"a":[1]}`},
	}
	for _, tc := range tests {
		if got := string(reserialize([]byte(tc.in))); got != tc.want {
			t.Errorf("reserialize(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestJavaDoubleString checks the Double.toString forms the JDK writes.
func TestJavaDoubleString(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"100":                     "100.0",
		"1.5":                     "1.5",
		"0.1":                     "0.1",
		"0.001":                   "0.001",
		"0.0012":                  "0.0012",
		"1e-4":                    "1.0E-4",
		"9999999":                 "9999999.0",
		"1e7":                     "1.0E7",
		"12345678.9":              "1.23456789E7",
		"123456.789":              "123456.789",
		"1e20":                    "1.0E20",
		"-2.5e-10":                "-2.5E-10",
		"1.7976931348623157e308":  "1.7976931348623157E308",
		"4.9e-324":                "4.9E-324",
		"0":                       "0.0",
		"0.30000000000000004":     "0.30000000000000004",
		"123456789012345680000.0": "1.2345678901234568E20",
	} {
		f, err := strconv.ParseFloat(in, 64)
		if err != nil {
			t.Fatalf("ParseFloat(%s): %v", in, err)
		}
		if got := javaDoubleString(f); got != want {
			t.Errorf("javaDoubleString(%s) = %s, want %s", in, got, want)
		}
	}
}
