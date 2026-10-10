package form

import "strings"

// Get returns the value of key, walking the tree as a codec does.
func (v *Values) Get(key string) (string, bool) {
	n := v.Root()
	for seg := range strings.SplitSeq(key, ".") {
		next, ok := n.Child(seg)
		if !ok {
			return "", false
		}
		n = next
	}
	return n.Value()
}
