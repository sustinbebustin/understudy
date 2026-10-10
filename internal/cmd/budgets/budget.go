package main

import (
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"
)

// Quantity is a measured or ceiling amount in one of the budget units.
//
//sumtype:decl
type Quantity interface {
	isQuantity()
	// String renders the amount in its unit: 31.4 MiB, 182 ms, 74.0 s.
	String() string
}

// Bytes is a size or a memory amount.
type Bytes int64

// Duration is a wall time.
type Duration time.Duration

// Count is a number of things, such as journal events.
type Count int64

func (b Bytes) String() string { return strconv.FormatFloat(float64(b)/(1<<20), 'f', 1, 64) + " MiB" }

func (d Duration) String() string {
	if time.Duration(d) < time.Second {
		return strconv.FormatInt(time.Duration(d).Milliseconds(), 10) + " ms"
	}
	return strconv.FormatFloat(time.Duration(d).Seconds(), 'f', 1, 64) + " s"
}

func (c Count) String() string { return strconv.FormatInt(int64(c), 10) }

func (Bytes) isQuantity()    {}
func (Duration) isQuantity() {}
func (Count) isQuantity()    {}

// Budget is one row of ARCHITECTURE.md 5.7: what is measured and its
// ceiling.
type Budget struct {
	Name    string
	Ceiling Quantity
}

// Result is one budget's outcome: the measured value, or why it could not be
// measured.
//
//sumtype:decl
type Result interface{ isResult() }

// Measured is a value that was taken.
type Measured struct{ Value Quantity }

// Failed is a measurement that could not be taken; the budget then fails,
// since an unmeasured budget is not a kept one.
type Failed struct{ Err error }

func (Measured) isResult() {}
func (Failed) isResult()   {}

// Row is a budget with its result.
type Row struct {
	Budget Budget
	Result Result
}

// Within reports whether the row passes: measured, in the ceiling's unit,
// and at or under it.
func (r Row) Within() bool {
	m, ok := r.Result.(Measured)
	if !ok {
		return false
	}
	switch c := r.Budget.Ceiling.(type) {
	case Bytes:
		v, ok := m.Value.(Bytes)
		return ok && v <= c
	case Duration:
		v, ok := m.Value.(Duration)
		return ok && v <= c
	case Count:
		v, ok := m.Value.(Count)
		return ok && v <= c
	default:
		return false
	}
}

// The budgets of ARCHITECTURE.md 5.7.
var (
	budgetBinary     = Budget{"stripped linux/amd64 binary", Bytes(48 << 20)}
	budgetStartup    = Budget{"serve start to a ready /health", Duration(250 * time.Millisecond)}
	budgetIdleRSS    = Budget{"idle RSS after start, no Docker", Bytes(128 << 20)}
	budgetColdBuild  = Budget{"cold go build ./...", Duration(180 * time.Second)}
	budgetLint       = Budget{"golangci-lint run ./... wall time", Duration(600 * time.Second)}
	budgetRingEvents = Budget{"journal ring default, events", Count(100_000)}
	budgetRingBytes  = Budget{"journal ring default, bytes", Bytes(128 << 20)}
)

// writeTable prints the rows and reports whether every one is within its
// ceiling.
func writeTable(w io.Writer, rows []Row) (bool, error) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	ok := true
	if _, err := fmt.Fprintln(tw, "budget\tmeasured\tceiling\tresult"); err != nil {
		return false, err
	}
	for _, r := range rows {
		var value, verdict string
		switch res := r.Result.(type) {
		case Measured:
			value, verdict = res.Value.String(), "ok"
			if !r.Within() {
				verdict = "OVER"
			}
		case Failed:
			value, verdict = "not measured: "+res.Err.Error(), "FAILED"
		}
		if !r.Within() {
			ok = false
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Budget.Name, value, r.Budget.Ceiling, verdict); err != nil {
			return false, err
		}
	}
	return ok, tw.Flush()
}
