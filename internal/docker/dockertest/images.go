package dockertest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// Remote is an image a pull can fetch.
type Remote struct {
	// Image is what becomes present locally after the pull.
	Image docker.Image
	// Layers are the layer IDs the progress stream reports.
	Layers []string
	// Error, when set, fails the pull mid-stream with an errorDetail frame
	// after the first line, as a registry failure does.
	Error string
}

// AddImage makes an image present locally under ref.
func (d *Daemon) AddImage(ref string, img docker.Image) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addImage(ref, img)
}

func (d *Daemon) addImage(ref string, img docker.Image) {
	ref = normalizeRef(ref)
	if img.ID == "" {
		sum := sha256.Sum256([]byte(ref))
		img.ID = "sha256:" + hex.EncodeToString(sum[:])
	}
	if !slices.Contains(img.RepoTags, ref) {
		img.RepoTags = append(img.RepoTags, ref)
	}
	d.images[ref] = img
}

// AddRemoteImage makes ref pullable.
func (d *Daemon) AddRemoteImage(ref string, r Remote) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.remote[normalizeRef(ref)] = r
}

// normalizeRef adds the implicit "latest" tag to a reference with neither
// tag nor digest.
func normalizeRef(ref string) string {
	if strings.Contains(ref, "@") {
		return ref
	}
	if strings.Contains(ref[strings.LastIndex(ref, "/")+1:], ":") {
		return ref
	}
	return ref + ":latest"
}

// lookupImage finds a local image by reference or ID. Callers hold d.mu.
func (d *Daemon) lookupImage(ref string) (docker.Image, bool) {
	if img, ok := d.images[normalizeRef(ref)]; ok {
		return img, true
	}
	for k := range d.images {
		if id := d.images[k].ID; id == ref || strings.TrimPrefix(id, "sha256:") == ref {
			return d.images[k], true
		}
	}
	return docker.Image{}, false
}

func (d *Daemon) imageInspect(w http.ResponseWriter, r *http.Request) {
	ref, ok := strings.CutSuffix(r.PathValue("ref"), "/json")
	if !ok {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	d.mu.Lock()
	img, found := d.lookupImage(ref)
	d.mu.Unlock()
	if !found {
		writeError(w, http.StatusNotFound, "No such image: "+ref)
		return
	}
	writeJSON(w, http.StatusOK, img)
}

type pullFrame struct {
	Status         string          `json:"status,omitempty"`
	ID             string          `json:"id,omitempty"`
	ProgressDetail *struct{}       `json:"progressDetail,omitempty"`
	ErrorDetail    *pullErrorFrame `json:"errorDetail,omitempty"`
	Error          string          `json:"error,omitempty"`
}

type pullErrorFrame struct {
	Message string `json:"message"`
}

// imagePull streams the progress frames the containerd image store sends
// (containers.md 2.4).
func (d *Daemon) imagePull(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref := q.Get("fromImage")
	if tag := q.Get("tag"); tag != "" {
		ref += ":" + tag
	}
	ref = normalizeRef(ref)
	d.mu.Lock()
	remote, ok := d.remote[ref]
	_, present := d.images[ref]
	d.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, `failed to resolve reference "`+ref+`": `+ref+`: not found`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	repo, tag := splitTag(ref)
	frames := []pullFrame{{Status: "Pulling from " + repo, ID: tag}}
	if remote.Error != "" {
		frames = append(frames, pullFrame{ErrorDetail: &pullErrorFrame{Message: remote.Error}, Error: remote.Error})
		writeFrames(w, frames)
		return
	}
	if present {
		frames = append(frames, pullFrame{Status: "Status: Image is up to date for " + ref})
		writeFrames(w, frames)
		return
	}
	for _, layer := range remote.Layers {
		frames = append(frames,
			pullFrame{Status: "Pulling fs layer", ID: layer, ProgressDetail: &struct{}{}},
			pullFrame{Status: "Download complete", ID: layer, ProgressDetail: &struct{}{}},
			pullFrame{Status: "Pull complete", ID: layer, ProgressDetail: &struct{}{}})
	}
	frames = append(frames, pullFrame{Status: "Status: Downloaded newer image for " + ref})
	d.mu.Lock()
	d.addImage(ref, remote.Image)
	d.mu.Unlock()
	writeFrames(w, frames)
}

// splitTag splits a normalized reference into repository and tag or digest.
func splitTag(ref string) (repo, tag string) {
	if repo, digest, ok := strings.Cut(ref, "@"); ok {
		return repo, digest
	}
	i := strings.LastIndex(ref, ":")
	return ref[:i], ref[i+1:]
}

func writeFrames(w http.ResponseWriter, frames []pullFrame) {
	for _, f := range frames {
		raw, err := json.Marshal(f)
		if err != nil || !writeBody(w, append(raw, '\n')) {
			return
		}
	}
}
