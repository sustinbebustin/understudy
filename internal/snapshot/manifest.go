package snapshot

import (
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Format is the archive format this binary writes and the newest it reads.
// It stays 0 until the first tagged release; from then on a change bumps it
// and adds a migration (ADR 0009).
const Format = 0

// Archive entry names outside the per-service sections.
const (
	manifestName = "manifest.json"
	faultsName   = "faults.json"
	journalName  = "journal.ndjson"
	servicesDir  = "services/"
	blobsDir     = "blobs/sha256/"
)

// Manifest is the archive's manifest.json: what wrote the archive, the
// non-cell state that must be restored with the cells, and the list of
// sections with their schema versions and digests.
type Manifest struct {
	// Format is the archive format. Read and Load refuse one newer than
	// Format; Write ignores the value given and writes Format.
	Format int
	// Writer names the understudy build that wrote the archive.
	Writer string
	// ModelPin is the commit of the vendored Smithy models the writer was
	// generated from.
	ModelPin string
	// Clock is emulator time at the cut.
	Clock Clock
	// Seed is the id source's seed, or nil when ids came from crypto/rand.
	Seed *uint64
	// Positions are the seeded id streams' positions; only a seeded source
	// has them.
	Positions map[ids.Kind]uint64
	// JournalNext is the id the journal assigns next.
	JournalNext ref.EventID
	// LateServices are the Late services whose data the save captured,
	// sorted. Every late section belongs to one of them; a listed service
	// may have no sections (it held no data). Load replaces the late data of
	// exactly these services, so an archive that lists none, such as the
	// --persist checkpoint, leaves late data alone.
	LateServices []ref.ServiceID
	// Blobs says whether the archive embeds its blobs or the blob store
	// keeps them. Read fills it; Write sets it from WriteOptions.Blobs and
	// ignores the value given.
	Blobs BlobPlacement
	// Sections describes every section, sorted by path. Read fills it from
	// the archive; Write derives it from Archive.Sections and Archive.Late
	// and ignores the value given.
	Sections []SectionMeta
}

// Clock is emulator time and the kind of mode the clock ran in. Only the
// kind of the mode and a Scaled factor are recorded: a mode's offset and
// anchors are tied to the wall clock of the process that recorded them, so
// they would make every save differ, and clock.Clock.Restore ignores them.
// Read returns Running{}, Frozen{At: At} or Scaled{Factor: f}.
type Clock struct {
	Mode clock.Mode
	At   clock.Instant
}

// SectionMeta describes one section in the manifest.
type SectionMeta struct {
	// Path is the section's archive entry name, "services/<svc>/...".
	Path string
	// Version is the schema version of the state that wrote the section.
	Version int
	// SHA256 is the lowercase hex digest of the section's bytes.
	SHA256 string
	// Blobs are the blob references the section pins, one per reference
	// and sorted, as store.Section.Blobs holds them.
	Blobs []string
	// Late marks a section a LateExporter wrote after the cut. A late
	// section pins no blobs.
	Late bool
}

// Clock mode names in the manifest.
const (
	modeRunning = "running"
	modeFrozen  = "frozen"
	modeScaled  = "scaled"
)

// Blob placement names in the manifest.
const (
	placementEmbedded = "embedded"
	placementExternal = "external"
)

type manifestJSON struct {
	Format       int                 `json:"format"`
	Writer       string              `json:"writer"`
	ModelPin     string              `json:"modelPin,omitempty"`
	Clock        clockJSON           `json:"clock"`
	Seed         *uint64             `json:"seed,omitempty"`
	Positions    map[ids.Kind]uint64 `json:"positions,omitempty"`
	JournalNext  ref.EventID         `json:"journalNext"`
	LateServices []ref.ServiceID     `json:"lateServices,omitempty"`
	Blobs        string              `json:"blobs"`
	Sections     []sectionJSON       `json:"sections,omitempty"`
}

type clockJSON struct {
	Mode   string        `json:"mode"`
	Factor float64       `json:"factor,omitzero"`
	At     clock.Instant `json:"at"`
}

type sectionJSON struct {
	Path    string   `json:"path"`
	Version int      `json:"version"`
	SHA256  string   `json:"sha256"`
	Blobs   []string `json:"blobs,omitempty"`
	Late    bool     `json:"late,omitzero"`
}

// encodeManifest renders m as deterministic JSON. It refuses a manifest Read
// would refuse, so no archive is written that cannot be read back.
func encodeManifest(m Manifest) ([]byte, error) {
	if err := checkManifest(m); err != nil {
		return nil, err
	}
	c, err := encodeClock(m.Clock)
	if err != nil {
		return nil, err
	}
	doc := manifestJSON{
		Format:       m.Format,
		Writer:       m.Writer,
		ModelPin:     m.ModelPin,
		Clock:        c,
		Seed:         m.Seed,
		Positions:    m.Positions,
		JournalNext:  m.JournalNext,
		LateServices: m.LateServices,
		Blobs:        m.Blobs.String(),
	}
	for _, s := range m.Sections {
		doc.Sections = append(doc.Sections, sectionJSON(s))
	}
	b, err := jsonx.Marshal(doc)
	if err != nil {
		return nil, &ManifestError{Reason: "it does not encode as JSON", Err: err}
	}
	return b, nil
}

// decodeManifest parses manifest.json. The format is checked first, from a
// lenient read of format and writer alone, so a newer archive is refused
// with a *FormatError even when the rest of its manifest has changed shape.
func decodeManifest(data []byte) (Manifest, error) {
	format, writer, err := peekFormat(data)
	if err != nil {
		return Manifest{}, err
	}
	if format > Format {
		return Manifest{}, &FormatError{Archive: format, Reader: Format, Writer: writer}
	}
	var doc manifestJSON
	if err := jsonx.Unmarshal(data, &doc); err != nil {
		return Manifest{}, &ManifestError{Reason: "it does not match the manifest layout", Err: err}
	}
	c, err := decodeClock(doc.Clock)
	if err != nil {
		return Manifest{}, err
	}
	placement, err := decodePlacement(doc.Blobs)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{
		Format:       doc.Format,
		Writer:       doc.Writer,
		ModelPin:     doc.ModelPin,
		Clock:        c,
		Seed:         doc.Seed,
		Positions:    doc.Positions,
		JournalNext:  doc.JournalNext,
		LateServices: doc.LateServices,
		Blobs:        placement,
	}
	for _, s := range doc.Sections {
		m.Sections = append(m.Sections, SectionMeta(s))
	}
	if err := checkManifest(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// peekFormat reads the top-level format and writer members, skipping every
// other member.
func peekFormat(data []byte) (format int, writer string, err error) {
	d := jsonx.NewDecoder(data)
	if err := d.BeginObject(); err != nil {
		return 0, "", &ManifestError{Reason: "it is not a JSON object", Err: err}
	}
	format = -1
	for {
		name, ok, err := d.NextName()
		if err != nil {
			return 0, "", &ManifestError{Reason: "it is not valid JSON", Err: err}
		}
		if !ok {
			break
		}
		switch name {
		case "format":
			format, err = readFormat(d)
		case "writer":
			if writer, err = d.ReadString(); err != nil {
				err = &ManifestError{Reason: "writer is not a string", Err: err}
			}
		default:
			if err = d.Skip(); err != nil {
				err = &ManifestError{Reason: "it is not valid JSON", Err: err}
			}
		}
		if err != nil {
			return 0, "", err
		}
	}
	if format < 0 {
		return 0, "", &ManifestError{Reason: "it has no format member"}
	}
	return format, writer, nil
}

func readFormat(d *jsonx.Decoder) (int, error) {
	n, err := d.ReadNumber()
	if err != nil {
		return 0, &ManifestError{Reason: "format is not a number", Err: err}
	}
	v, err := n.Int64()
	if err != nil || v < 0 || v > math.MaxInt32 {
		return 0, &ManifestError{Reason: fmt.Sprintf("format %s is not a non-negative 32-bit integer", n), Err: err}
	}
	return int(v), nil
}

func encodeClock(c Clock) (clockJSON, error) {
	switch m := c.Mode.(type) {
	case clock.Running:
		return clockJSON{Mode: modeRunning, At: c.At}, nil
	case clock.Frozen:
		return clockJSON{Mode: modeFrozen, At: c.At}, nil
	case clock.Scaled:
		if !(m.Factor > 0 && m.Factor <= clock.MaxScale) {
			return clockJSON{}, &ManifestError{Reason: fmt.Sprintf("clock scale factor %v is outside (0, %d]", m.Factor, clock.MaxScale)}
		}
		return clockJSON{Mode: modeScaled, Factor: m.Factor, At: c.At}, nil
	}
	return clockJSON{}, &ManifestError{Reason: fmt.Sprintf("clock mode %T is not Running, Frozen or Scaled", c.Mode)}
}

func decodeClock(c clockJSON) (Clock, error) {
	var m clock.Mode
	switch c.Mode {
	case modeRunning:
		m = clock.Running{}
	case modeFrozen:
		m = clock.Frozen{At: c.At}
	case modeScaled:
		m = clock.Scaled{Factor: c.Factor}
	default:
		return Clock{}, &ManifestError{Reason: fmt.Sprintf("clock mode %q is not %s, %s or %s", c.Mode, modeRunning, modeFrozen, modeScaled)}
	}
	if c.Mode != modeScaled && c.Factor != 0 {
		return Clock{}, &ManifestError{Reason: fmt.Sprintf("clock mode %q has a scale factor", c.Mode)}
	}
	out := Clock{Mode: m, At: c.At}
	if _, err := encodeClock(out); err != nil {
		return Clock{}, err
	}
	return out, nil
}

func decodePlacement(name string) (BlobPlacement, error) {
	switch name {
	case placementEmbedded:
		return EmbedBlobs, nil
	case placementExternal:
		return ExternalBlobs, nil
	}
	return 0, &ManifestError{Reason: fmt.Sprintf("blob placement %q is not %s or %s", name, placementEmbedded, placementExternal)}
}

// checkManifest refuses what the rest of the emulator could not restore: a
// format other than one this binary reads, a missing blob placement, stream
// positions without a seed or above ids.MaxPosition, a late service list
// that is unsorted, repeated or misses the service of a late section, and
// section metadata that is unsorted, duplicated, names no valid section path,
// lists its blobs unsorted or is late and pins blobs. Read, Write, Load and
// Diff share it.
func checkManifest(m Manifest) error {
	switch {
	case m.Format < 0:
		return &ManifestError{Reason: fmt.Sprintf("format %d is negative", m.Format)}
	case m.Format > Format:
		return &FormatError{Archive: m.Format, Reader: Format, Writer: m.Writer}
	}
	switch m.Blobs {
	case EmbedBlobs, ExternalBlobs:
	default:
		return &ManifestError{Reason: fmt.Sprintf("blob placement %v is neither EmbedBlobs nor ExternalBlobs", m.Blobs)}
	}
	if m.Seed == nil && len(m.Positions) > 0 {
		return &ManifestError{Reason: "it records id stream positions but no seed; only a seeded source has positions"}
	}
	for k, pos := range m.Positions {
		if pos > ids.MaxPosition {
			return &ManifestError{Reason: fmt.Sprintf("the %s stream position %d is above the %d a load can restore", k, pos, uint64(ids.MaxPosition))}
		}
	}
	for i, svc := range m.LateServices {
		if !validServiceName(svc) {
			return &ManifestError{Reason: fmt.Sprintf("late service name %q is not one clean path element", svc)}
		}
		if i > 0 && svc <= m.LateServices[i-1] {
			return &ManifestError{Reason: fmt.Sprintf("late service %q is listed out of order or twice", svc)}
		}
	}
	for i, s := range m.Sections {
		if i > 0 && s.Path <= m.Sections[i-1].Path {
			return &ManifestError{Reason: fmt.Sprintf("section %q is listed out of order or twice", s.Path)}
		}
		if err := checkSection(s, m.LateServices); err != nil {
			return err
		}
	}
	return nil
}

// checkSection refuses section metadata with an invalid path, digest or
// blob reference, blobs out of order, or a late section of a service
// lateServices does not list or that pins blobs.
func checkSection(s SectionMeta, lateServices []ref.ServiceID) error {
	svc, err := sectionService(s.Path)
	if err != nil {
		return err
	}
	if s.Late && !slices.Contains(lateServices, svc) {
		return &ManifestError{Reason: fmt.Sprintf("late section %q belongs to service %q, which is not listed in lateServices", s.Path, svc)}
	}
	// Load retains only the blobs of the cut sections, so a blob a late
	// section pinned would be pruned once the load ends.
	if s.Late && len(s.Blobs) > 0 {
		return &ManifestError{Reason: fmt.Sprintf("late section %q pins blobs; late data holds no blobs", s.Path)}
	}
	if _, err := blob.ParseRef(s.SHA256); err != nil {
		return &ManifestError{Reason: fmt.Sprintf("section %q has an invalid sha256", s.Path), Err: err}
	}
	if !slices.IsSorted(s.Blobs) {
		return &ManifestError{Reason: fmt.Sprintf("section %q lists its blobs unsorted; store.Section.Blobs is sorted", s.Path)}
	}
	for _, b := range s.Blobs {
		if _, err := blob.ParseRef(b); err != nil {
			return &ManifestError{Reason: fmt.Sprintf("section %q pins an invalid blob reference", s.Path), Err: err}
		}
	}
	return nil
}

// validServiceName reports whether name is one clean, local path element,
// as a service's directory under services/ must be. fs.ValidPath accepts
// ".", which names no directory.
func validServiceName(name ref.ServiceID) bool {
	return name != "" && name != "." && !strings.Contains(string(name), "/") && fs.ValidPath(string(name))
}

// sectionService returns the service a section path belongs to. A path is
// "services/<svc>/<rest>" with every element a valid, local path element.
func sectionService(path string) (ref.ServiceID, error) {
	rest, ok := strings.CutPrefix(path, servicesDir)
	svc, tail, found := strings.Cut(rest, "/")
	if !ok || !found || svc == "" || tail == "" || !fs.ValidPath(path) || !filepath.IsLocal(path) {
		return "", &EntryError{Name: path, Defect: BadSectionPath}
	}
	return ref.ServiceID(svc), nil
}
