package cli

import (
	"io"
	"strings"
	"text/tabwriter"
)

// table aligns tab-separated cells into columns, as every text table of
// the CLI does, without the trailing padding tabwriter leaves on a row
// whose last cells are empty.
type table struct {
	*tabwriter.Writer
	buf *strings.Builder
	out io.Writer
}

func newTable(w io.Writer) *table {
	buf := &strings.Builder{}
	return &table{Writer: tabwriter.NewWriter(buf, 0, 4, 2, ' ', 0), buf: buf, out: w}
}

// Flush writes the aligned rows to the table's writer.
func (t *table) Flush() error {
	if err := t.Writer.Flush(); err != nil {
		return err
	}
	var b strings.Builder
	for line := range strings.Lines(t.buf.String()) {
		b.WriteString(strings.TrimRight(line, " \n") + "\n")
	}
	t.buf.Reset()
	_, err := io.WriteString(t.out, b.String())
	return err
}
