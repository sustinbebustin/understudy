package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseVMRSSReadsKibibytes(t *testing.T) {
	status := "Name:\tunderstudy\nVmPeak:\t  900000 kB\nVmRSS:\t   23552 kB\nThreads:\t12\n"
	got, err := parseVMRSS(strings.NewReader(status))
	if err != nil || got != Bytes(23552<<10) {
		t.Fatalf("parseVMRSS = %v, %v; want 23 MiB", got, err)
	}
	for _, bad := range []string{"Name:\tx\n", "VmRSS:\t12 MB\n", "VmRSS:\tlots kB\n"} {
		if _, err := parseVMRSS(strings.NewReader(bad)); err == nil {
			t.Errorf("parseVMRSS(%q) accepted a status without a usable VmRSS line", bad)
		}
	}
}

// TestRowWithin: a row passes only when measured, in its ceiling's unit, at
// or under the ceiling.
func TestRowWithin(t *testing.T) {
	ceiling := Budget{"x", Bytes(48 << 20)}
	tests := []struct {
		result Result
		want   bool
	}{
		{Measured{Bytes(48 << 20)}, true},
		{Measured{Bytes(48<<20 + 1)}, false},
		{Measured{Duration(time.Millisecond)}, false},
		{Failed{errors.New("no binary")}, false},
	}
	for _, tt := range tests {
		if got := (Row{ceiling, tt.result}).Within(); got != tt.want {
			t.Errorf("Within(%v) = %v, want %v", tt.result, got, tt.want)
		}
	}
}

func TestWriteTableMarksEveryRow(t *testing.T) {
	var b strings.Builder
	ok, err := writeTable(&b, []Row{
		{budgetBinary, Measured{Bytes(31 << 20)}},
		{budgetStartup, Measured{Duration(312 * time.Millisecond)}},
		{budgetColdBuild, Failed{errors.New("go build ./...: exit status 1")}},
		{budgetRingEvents, Measured{Count(100_000)}},
	})
	if err != nil || ok {
		t.Fatalf("writeTable = %v, %v; want a failing table", ok, err)
	}
	for _, want := range []string{
		"stripped linux/amd64 binary     31.0 MiB",
		"serve start to a ready /health  312 ms",
		"not measured: go build ./...: exit status 1",
		"journal ring default, events    100000",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, b.String())
		}
	}
	for verdict, want := range map[string]int{"ok": 2, "OVER": 1, "FAILED": 1} {
		if got := strings.Count(b.String(), "  "+verdict+"\n"); got != want {
			t.Errorf("table marks %d rows %s, want %d (OVER the slow start, FAILED the build not measured):\n%s", got, verdict, want, b.String())
		}
	}
}

func TestDurationRendersInItsScale(t *testing.T) {
	if got := Duration(74 * time.Second).String(); got != "74.0 s" {
		t.Errorf("74 s renders %q", got)
	}
	if got := Duration(182 * time.Millisecond).String(); got != "182 ms" {
		t.Errorf("182 ms renders %q", got)
	}
}
