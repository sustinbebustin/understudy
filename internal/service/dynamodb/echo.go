package dynamodb

import (
	"slices"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// tableEcho puts back into an item operation's answer the table ARNs its
// request named tables by. Local answers with each table's bare name
// wherever it names one: the keys of Responses, UnprocessedKeys,
// UnprocessedItems and ItemCollectionMetrics, and ConsumedCapacity's
// TableName. AWS echoes the identifier the caller used (API_ConsumedCapacity,
// API_BatchGetItem: "A map of table name or table ARN"), so a caller that
// reads Responses[tableArn] finds its items.
type tableEcho struct {
	// arns maps a bare table name to the ARN the request named it by.
	arns map[string]string
	// keyed are the answer's top-level members whose keys are tables.
	keyed []string
}

// echoed is the answer of an item operation naming the tables names lists,
// whose answer members keyed hold maps keyed by table: names the request
// gave as ARNs are echoed back (tableEcho), and nothing else changes.
func echoed[In any](names func(*In) []string, keyed ...string) func(*In) answer {
	return func(in *In) answer { return answer{echo: echoOf(names(in), keyed)} }
}

// echoOf is the echo of a request naming the tables names, nil when it
// names every table by its bare name. A table the request names both by
// its name and by its ARN, or by two spellings of its ARN, keeps Local's
// bare name: which one AWS answers with is unknown.
func echoOf(names, keyed []string) *tableEcho {
	used := make(map[string]string, len(names))
	mixed := map[string]bool{}
	for _, n := range names {
		bare := tableName(n)
		if was, seen := used[bare]; seen && was != n {
			mixed[bare] = true
		}
		used[bare] = n
	}
	arns := map[string]string{}
	for bare, n := range used {
		if n != bare && !mixed[bare] {
			arns[bare] = n
		}
	}
	if len(arns) == 0 {
		return nil
	}
	return &tableEcho{arns: arns, keyed: keyed}
}

// apply renames the bare table names Local answered with in m, in place,
// and reports whether it renamed one. Item data is never read.
func (e *tableEcho) apply(m document.Map) bool {
	changed := false
	for _, entry := range m {
		switch {
		case entry.Key == "ConsumedCapacity":
			changed = e.consumed(entry.Value) || changed
		case slices.Contains(e.keyed, entry.Key):
			if tables, ok := entry.Value.(document.Map); ok {
				changed = e.rekey(tables) || changed
			}
		}
	}
	return changed
}

// consumed renames TableName in a ConsumedCapacity: one object on the
// single-table operations, a list of them on the batch and transaction
// operations.
func (e *tableEcho) consumed(v document.Value) bool {
	switch v := v.(type) {
	case document.Map:
		return e.renameTableName(v)
	case document.List:
		changed := false
		for _, x := range v {
			if m, ok := x.(document.Map); ok {
				changed = e.renameTableName(m) || changed
			}
		}
		return changed
	case document.Null, document.Bool, document.Number, document.String:
	}
	return false
}

func (e *tableEcho) renameTableName(capacity document.Map) bool {
	for i, entry := range capacity {
		if entry.Key != "TableName" {
			continue
		}
		name, ok := entry.Value.(document.String)
		if !ok {
			return false
		}
		arn, ok := e.arns[string(name)]
		if !ok {
			return false
		}
		capacity[i].Value = document.String(arn)
		return true
	}
	return false
}

// rekey renames the keys of tables, a map keyed by table.
func (e *tableEcho) rekey(tables document.Map) bool {
	changed := false
	for i, entry := range tables {
		if arn, ok := e.arns[entry.Key]; ok {
			tables[i].Key = arn
			changed = true
		}
	}
	return changed
}
