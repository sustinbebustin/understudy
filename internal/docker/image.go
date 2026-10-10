package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Platform is an OCI platform such as linux/amd64. Lambda's x86_64 and
// arm64 architectures map to linux/amd64 and linux/arm64.
type Platform struct {
	OS           string
	Architecture string
	Variant      string
}

// String formats the platform as os/architecture[/variant], or "" for the
// zero value (the daemon's own platform).
func (p Platform) String() string {
	if p == (Platform{}) {
		return ""
	}
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// Image is the subset of GET /images/{name}/json that launching reads.
// Since API 1.52 unset Config members are omitted rather than null, so
// absent and empty are equal here.
type Image struct {
	ID           string      `json:"Id"`
	RepoTags     []string    `json:"RepoTags"`
	RepoDigests  []string    `json:"RepoDigests"`
	OS           string      `json:"Os"`
	Architecture string      `json:"Architecture"`
	Variant      string      `json:"Variant,omitempty"`
	Size         int64       `json:"Size"`
	Config       ImageConfig `json:"Config"`
}

// ImageConfig is the runtime configuration baked into an image.
type ImageConfig struct {
	Entrypoint []string          `json:"Entrypoint,omitempty"`
	Cmd        []string          `json:"Cmd,omitempty"`
	Env        []string          `json:"Env,omitempty"`
	WorkingDir string            `json:"WorkingDir,omitempty"`
	User       string            `json:"User,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
}

// ImageInspect reads a local image by reference or ID. An absent image is an
// *Error for which IsNotFound holds.
func (c *Client) ImageInspect(ctx context.Context, ref string) (Image, error) {
	var img Image
	err := c.callJSON(ctx, "ImageInspect", http.MethodGet, "/images/"+ref+"/json", nil, nil, &img)
	return img, err
}

// Progress is one frame of a pull's JSON progress stream. Only layer
// counts and the terminal "Status:" line are a contract; byte-level
// progress is informational and may be absent (containers.md 2.4).
type Progress struct {
	ID      string
	Status  string
	Current int64
	Total   int64
}

type progressFrame struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	ProgressDetail struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
	ErrorDetail *struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
	Error string `json:"error"`
}

// ImagePull pulls ref (anonymously) for platform, calling progress for each
// frame when it is non-nil. A reference without a tag or digest pulls
// "latest", never every tag. A failure the daemon reports after the stream
// has started is a *StreamError.
func (c *Client) ImagePull(ctx context.Context, ref string, platform Platform, progress func(Progress)) error {
	from, tag := splitReference(ref)
	q := url.Values{"fromImage": {from}}
	if tag != "" {
		q.Set("tag", tag)
	}
	if p := platform.String(); p != "" {
		q.Set("platform", p)
	}
	resp, err := c.call(ctx, "ImagePull", http.MethodPost, "/images/create", q, nil, "")
	if err != nil {
		return err
	}
	defer drain(resp)
	dec := json.NewDecoder(resp.Body)
	for {
		var f progressFrame
		if err := dec.Decode(&f); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("docker ImagePull %s: read progress stream: %w", ref, err)
		}
		if f.ErrorDetail != nil || f.Error != "" {
			return &StreamError{Op: "ImagePull " + ref, Message: pullErrorMessage(f)}
		}
		if progress != nil {
			progress(Progress{ID: f.ID, Status: f.Status, Current: f.ProgressDetail.Current, Total: f.ProgressDetail.Total})
		}
	}
}

func pullErrorMessage(f progressFrame) string {
	if f.ErrorDetail != nil && f.ErrorDetail.Message != "" {
		return f.ErrorDetail.Message
	}
	return f.Error
}

// splitReference splits an image reference into the fromImage and tag query
// values of a pull. A digest reference is sent whole with no tag; a
// reference with neither tag nor digest gets "latest", because an empty tag
// pulls every tag of the repository.
func splitReference(ref string) (from, tag string) {
	if strings.Contains(ref, "@") {
		return ref, ""
	}
	nameStart := strings.LastIndex(ref, "/") + 1
	if i := strings.LastIndex(ref[nameStart:], ":"); i >= 0 {
		return ref[:nameStart+i], ref[nameStart+i+1:]
	}
	return ref, "latest"
}
