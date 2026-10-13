package dynamodb

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/store"
)

// tableKey names one table: the TTL sweep timer's id and the snapshot
// section of its items are its path.
type tableKey struct {
	scope aws.Scope
	table string
}

// path is "<account>/<region>/<table>". A table name holds no slash, so
// the last one splits it.
func (k tableKey) path() string { return store.ScopeKeys().Encode(k.scope) + "/" + k.table }

// parseTableKey reads a path back.
func parseTableKey(path string) (tableKey, error) {
	i := strings.LastIndexByte(path, '/')
	if i < 0 || i == len(path)-1 {
		return tableKey{}, &TableKeyError{Path: path}
	}
	scope, err := store.ScopeKeys().Decode(path[:i])
	if err != nil {
		return tableKey{}, &TableKeyError{Path: path, Err: err}
	}
	return tableKey{scope: scope, table: path[i+1:]}, nil
}

// TableKeyError is a timer id or section path that names no table.
type TableKeyError struct {
	Path string
	Err  error
}

func (e *TableKeyError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%q names no table: the layout is <account>/<region>/<table>", e.Path)
	}
	return fmt.Sprintf("%q names no table: the layout is <account>/<region>/<table>: %v", e.Path, e.Err)
}

func (e *TableKeyError) Unwrap() error { return e.Err }
