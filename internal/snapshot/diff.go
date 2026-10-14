package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// Change says how an entry or a value differs between two archives.
type Change uint8

// Change values.
const (
	// Added: only the second archive has it.
	Added Change = iota + 1
	// Removed: only the first archive has it.
	Removed
	// Modified: both have it with different values.
	Modified
)

// String returns "added", "removed" or "modified".
func (c Change) String() string {
	switch c {
	case Added:
		return "added"
	case Removed:
		return "removed"
	case Modified:
		return "modified"
	default:
		return fmt.Sprintf("Change(%d)", uint8(c))
	}
}

// Difference is one difference between two archives: an entry only one has,
// or a value inside an entry both have.
type Difference struct {
	// Entry is the archive entry: manifest.json, faults.json, a section
	// path or blobs/sha256/<hex>. Under normalization, the account in a
	// section path is masked and the path numbered (package documentation),
	// as in services/sqs/<account>/us-east-1.json#1.
	Entry string
	// Pointer locates the value in the entry's document (RFC 6901). It is
	// empty when the whole entry was added or removed, or when the whole
	// document differs. A section's document is {"version", "blobs",
	// "data"}, data holding the section's JSON (an NDJSON section as an
	// array of its lines); manifest.json's is the manifest without its
	// section list, which the sections stand for.
	Pointer string
	Change  Change
	// Before and After are the values as compact JSON, empty on the side
	// that lacks the value.
	Before, After string
}

// Counter names a manifest value that moves whenever the server works
// between two saves, as opposed to resource state.
type Counter uint8

// The Counter values.
const (
	// NoCounter: the difference is not a manifest counter.
	NoCounter Counter = iota
	// JournalCounter: the manifest's journalNext, the id the journal
	// assigns next. Normalizing masks it.
	JournalCounter
	// PositionCounter: a seeded id stream's position, or the whole
	// positions object. Normalizing keeps it, since one seed and one
	// workload reproduce it, apart from the positions of the request-id
	// and lambda-request-id streams, whose names the request id rule
	// matches.
	PositionCounter
)

// Counter reports which manifest counter d is a difference of, if any.
func (d Difference) Counter() Counter {
	if d.Entry != manifestName {
		return NoCounter
	}
	switch {
	case d.Pointer == "/journalNext":
		return JournalCounter
	case d.Pointer == "/positions" || strings.HasPrefix(d.Pointer, "/positions/"):
		return PositionCounter
	}
	return NoCounter
}

// String renders the difference on one line: "+ entry pointer: after",
// "- entry pointer: before" or "~ entry pointer: before -> after".
func (d Difference) String() string {
	where := d.Entry
	if d.Pointer != "" {
		where += " " + d.Pointer
	}
	switch d.Change {
	case Added:
		if d.After == "" {
			return "+ " + where
		}
		return "+ " + where + ": " + d.After
	case Removed:
		if d.Before == "" {
			return "- " + where
		}
		return "- " + where + ": " + d.Before
	case Modified:
		return "~ " + where + ": " + d.Before + " -> " + d.After
	}
	return "? " + where
}

// Diff compares two archives entry by entry and value by value, in entry
// name order and then document order with members sorted by name. With
// normalize it first applies the closed normalizer set of the package
// documentation to both, so two runs of one workload compare equal. The
// journal is not compared; journals compare as canonical trace trees.
// An entry that is not valid JSON is compared by digest. The archives are
// those Read returned; Diff refuses one whose manifest Write would refuse
// (*ManifestError or *FormatError), naming which archive it is.
func Diff(a, b *Archive, normalize bool) ([]Difference, error) {
	ta, err := archiveTree(a, normalize)
	if err != nil {
		return nil, fmt.Errorf("snapshot diff: the first archive: %w", err)
	}
	tb, err := archiveTree(b, normalize)
	if err != nil {
		return nil, fmt.Errorf("snapshot diff: the second archive: %w", err)
	}
	var out []Difference
	diffMembers(ta.members, tb.members,
		func(name string, _ tree) { out = append(out, Difference{Entry: name, Change: Removed}) },
		func(name string, _ tree) { out = append(out, Difference{Entry: name, Change: Added}) },
		func(name string, x, y tree) { diffTrees(&out, name, "", x, y) })
	return out, nil
}

// archiveTree is the archive as an object with one member per entry.
func archiveTree(a *Archive, norm bool) (tree, error) {
	mt, err := manifestTree(a.Manifest, norm)
	if err != nil {
		return tree{}, err
	}
	ms := []member{{name: manifestName, val: mt}}
	if a.Faults != nil {
		ms = append(ms, member{name: faultsName, val: docTree(faultsName, a.Faults)})
	}
	for _, s := range slices.Concat(a.Sections, a.Late) {
		blobs := tree{kind: jsonx.Array}
		for _, r := range s.Blobs {
			blobs.elems = append(blobs.elems, str(r))
			ms = append(ms, member{name: blobsDir + r, val: tree{kind: jsonx.Null}})
		}
		ms = append(ms, member{name: s.Path, val: tree{kind: jsonx.Object, members: []member{
			{name: "blobs", val: blobs},
			{name: "data", val: docTree(s.Path, s.Data)},
			{name: "version", val: tree{kind: jsonx.Number, text: strconv.Itoa(s.Version)}},
		}}})
	}
	if !norm {
		return tree{kind: jsonx.Object, members: dedupe(ms)}, nil
	}
	ms = dedupe(ms)
	for i, m := range ms {
		ms[i].val = normalize(m.val, "", plainScope)
	}
	return tree{kind: jsonx.Object, members: disambiguate(ms, normalizeEntry)}, nil
}

// dedupe sorts entries by name and drops repeated blob entries, which every
// section pinning the blob adds.
func dedupe(ms []member) []member {
	sortMembers(ms)
	out := ms[:0]
	for i, m := range ms {
		if i > 0 && m.name == ms[i-1].name && strings.HasPrefix(m.name, blobsDir) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// manifestTree is the manifest without its section list. Under
// normalization a Running or Scaled clock's time is masked: it follows the
// wall clock.
func manifestTree(m Manifest, norm bool) (tree, error) {
	m.Sections = nil
	raw, err := encodeManifest(m)
	if err != nil {
		return tree{}, err
	}
	t := docTree(manifestName, raw)
	if _, frozen := m.Clock.Mode.(clock.Frozen); norm && !frozen {
		for i, c := range t.members {
			if c.name != "clock" {
				continue
			}
			for j, f := range c.val.members {
				if f.name == "at" {
					t.members[i].val.members[j].val = str(maskWallTime)
				}
			}
		}
	}
	return t, nil
}

// docTree parses an entry's document: NDJSON for a .ndjson entry, JSON
// otherwise. Bytes that do not parse stand in as their digest.
func docTree(name string, data []byte) tree {
	parse := parseTree
	if strings.HasSuffix(name, ".ndjson") {
		parse = parseLines
	}
	t, err := parse(data)
	if err != nil {
		sum := sha256.Sum256(data)
		return str("<not JSON, sha256 " + hex.EncodeToString(sum[:]) + ">")
	}
	return t
}

func diffTrees(out *[]Difference, entry, ptr string, a, b tree) {
	switch {
	case a.kind == jsonx.Object && b.kind == jsonx.Object:
		diffMembers(a.members, b.members,
			func(name string, v tree) {
				*out = append(*out, Difference{Entry: entry, Pointer: ptr + "/" + escape(name), Change: Removed, Before: v.json()})
			},
			func(name string, v tree) {
				*out = append(*out, Difference{Entry: entry, Pointer: ptr + "/" + escape(name), Change: Added, After: v.json()})
			},
			func(name string, x, y tree) { diffTrees(out, entry, ptr+"/"+escape(name), x, y) })
	case a.kind == jsonx.Array && b.kind == jsonx.Array:
		for i := range max(len(a.elems), len(b.elems)) {
			p := ptr + "/" + strconv.Itoa(i)
			switch {
			case i >= len(b.elems):
				*out = append(*out, Difference{Entry: entry, Pointer: p, Change: Removed, Before: a.elems[i].json()})
			case i >= len(a.elems):
				*out = append(*out, Difference{Entry: entry, Pointer: p, Change: Added, After: b.elems[i].json()})
			default:
				diffTrees(out, entry, p, a.elems[i], b.elems[i])
			}
		}
	case !equal(a, b):
		*out = append(*out, Difference{Entry: entry, Pointer: ptr, Change: Modified, Before: a.json(), After: b.json()})
	}
}

// diffMembers merges two member lists sorted by name.
func diffMembers(a, b []member, onlyA, onlyB func(string, tree), both func(string, tree, tree)) {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case j >= len(b) || i < len(a) && a[i].name < b[j].name:
			onlyA(a[i].name, a[i].val)
			i++
		case i >= len(a) || b[j].name < a[i].name:
			onlyB(b[j].name, b[j].val)
			j++
		default:
			both(a[i].name, a[i].val, b[j].val)
			i++
			j++
		}
	}
}

// escape encodes a JSON pointer reference token (RFC 6901).
func escape(name string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(name)
}
