package docker_test

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
)

const providedImage = "public.ecr.aws/lambda/provided:al2023"

func TestImageInspectOfAnAbsentImageIsNotFound(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{})
	_, err := d.Client(t).ImageInspect(t.Context(), providedImage)
	var derr *docker.Error
	if !docker.IsNotFound(err) || !errors.As(err, &derr) {
		t.Fatalf("ImageInspect error = %v, want not found", err)
	}
	if derr.Message != "No such image: "+providedImage || derr.Op != "ImageInspect" {
		t.Errorf("error = %+v, want the daemon's message", derr)
	}
}

func TestImageCallsRefuseAnInvalidReferenceWithoutSendingIt(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{})
	c := d.Client(t)
	const ref = "../containers/abc"
	var ierr *docker.InvalidReferenceError
	if _, err := c.ImageInspect(t.Context(), ref); !errors.As(err, &ierr) || ierr.Ref != ref {
		t.Errorf("ImageInspect(%q) = %v, want *InvalidReferenceError", ref, err)
	}
	if err := c.ImagePull(t.Context(), ref, docker.Platform{}, nil); !errors.As(err, &ierr) || ierr.Ref != ref {
		t.Errorf("ImagePull(%q) = %v, want *InvalidReferenceError", ref, err)
	}
	if reqs := d.Requests(); len(reqs) != 0 {
		t.Errorf("daemon received %v, want no request for an invalid reference", reqs)
	}
}

func TestImagePullStreamsProgressAndMakesTheImagePresent(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{})
	d.AddRemoteImage(providedImage, dockertest.Remote{
		Image:  docker.Image{OS: "linux", Architecture: "amd64", Config: docker.ImageConfig{Entrypoint: []string{"/lambda-entrypoint.sh"}}},
		Layers: []string{"bb84aa4bd282", "13e93ae8c725"},
	})
	c := d.Client(t)
	var statuses []string
	err := c.ImagePull(t.Context(), providedImage, docker.Platform{OS: "linux", Architecture: "amd64"}, func(p docker.Progress) {
		statuses = append(statuses, p.Status)
	})
	if err != nil {
		t.Fatalf("ImagePull: %v", err)
	}
	if got := countOf(statuses, "Pull complete"); got != 2 {
		t.Errorf("progress reported %d completed layers, want 2: %v", got, statuses)
	}
	if last := statuses[len(statuses)-1]; last != "Status: Downloaded newer image for "+providedImage {
		t.Errorf("last progress status = %q, want the terminal Status line", last)
	}
	img, err := c.ImageInspect(t.Context(), providedImage)
	if err != nil || img.Config.Entrypoint[0] != "/lambda-entrypoint.sh" {
		t.Fatalf("ImageInspect after pull = %+v, %v", img, err)
	}
	pull := lastRequest(d, http.MethodPost)
	if q := pull.Query; q.Get("fromImage") != "public.ecr.aws/lambda/provided" || q.Get("tag") != "al2023" || q.Get("platform") != "linux/amd64" {
		t.Errorf("pull query = %v, want fromImage, tag and platform split from the reference", q)
	}
}

func TestImagePullOfAnUntaggedReferencePullsLatestOnly(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{})
	d.AddRemoteImage("amazon/dynamodb-local", dockertest.Remote{})
	if err := d.Client(t).ImagePull(t.Context(), "amazon/dynamodb-local", docker.Platform{}, nil); err != nil {
		t.Fatalf("ImagePull: %v", err)
	}
	q := lastRequest(d, http.MethodPost).Query
	if q.Get("tag") != "latest" || q.Has("platform") {
		t.Errorf("pull query = %v, want tag=latest (an empty tag pulls every tag) and no platform", q)
	}
}

func TestImagePullFailures(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{})
	d.AddRemoteImage("example.com/broken:1", dockertest.Remote{Error: "unexpected EOF"})
	c := d.Client(t)

	err := c.ImagePull(t.Context(), "example.com/missing:1", docker.Platform{}, nil)
	if !docker.IsNotFound(err) {
		t.Errorf("pull of an unknown repository = %v, want not found before the stream starts", err)
	}
	err = c.ImagePull(t.Context(), "example.com/broken:1", docker.Platform{}, nil)
	var serr *docker.StreamError
	if !errors.As(err, &serr) || serr.Message != "unexpected EOF" {
		t.Errorf("pull failing mid-stream = %v, want *StreamError with the errorDetail message", err)
	}
}

func TestImagePullOfAPresentImageIsUpToDate(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{})
	d.AddImage(providedImage, docker.Image{})
	d.AddRemoteImage(providedImage, dockertest.Remote{})
	var statuses []string
	err := d.Client(t).ImagePull(t.Context(), providedImage, docker.Platform{}, func(p docker.Progress) { statuses = append(statuses, p.Status) })
	if err != nil || !slices.Contains(statuses, "Status: Image is up to date for "+providedImage) {
		t.Fatalf("ImagePull = %v, statuses %v; want the up-to-date line", err, statuses)
	}
}

func countOf(xs []string, x string) int {
	n := 0
	for _, s := range xs {
		if s == x {
			n++
		}
	}
	return n
}

func lastRequest(d *dockertest.Daemon, method string) dockertest.Request {
	reqs := d.Requests()
	for _, req := range slices.Backward(reqs) {
		if req.Method == method {
			return req
		}
	}
	return dockertest.Request{}
}
