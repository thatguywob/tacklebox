package purefs

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tuna-os/tacklebox/internal/oci"
)

type observationTransport func(*http.Request) (*http.Response, error)

func (f observationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func observationDigest(body string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body)))
}

func TestManifestObservationsUseConsumedBytes(t *testing.T) {
	body := `{"schemaVersion":2,"config":{"digest":"sha256:descriptor-only","size":42},"layers":[]}`
	digest := observationDigest(body)
	for _, indexed := range []bool{false, true} {
		t.Run(fmt.Sprint(indexed), func(t *testing.T) {
			index := fmt.Sprintf(`{"manifests":[{"digest":%q,"platform":{"architecture":"amd64","variant":"v2"}}]}`, digest)
			c := oci.NewClient("https://registry.invalid")
			calls := 0
			c.HTTP = &http.Client{Transport: observationTransport(func(r *http.Request) (*http.Response, error) {
				response := body
				switch r.URL.Path {
				case "/token":
					response = `{"token":"fixture"}`
				case "/v2/test/image/manifests/latest":
					calls++
					if indexed {
						response = index
					}
				case "/v2/test/image/manifests/" + digest:
					calls++
				default:
					t.Fatalf("unexpected fetch %s", r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
			})}
			m, observed, err := ResolveManifestObserved(c, oci.Ref{Repo: "test/image", Tag: "latest"}, "linux/amd64/v2")
			if err != nil {
				t.Fatal(err)
			}
			if m == nil || observed.ManifestDigest != digest || observed.Config.Descriptor.Digest != m.Config.Digest || observed.Config.BytesVerified {
				t.Fatalf("bad observation %+v", observed)
			}
			if indexed {
				if calls != 2 || observed.IndexDigest != observationDigest(index) || observed.PlatformDescriptor.Architecture != "amd64" || observed.PlatformDescriptor.Variant != "v2" {
					t.Fatalf("bad index observation %+v", observed)
				}
			} else if calls != 1 || observed.IndexDigest != "" || observed.PlatformDescriptor != nil {
				t.Fatalf("invented direct platform/index %+v", observed)
			}
		})
	}
}

func TestCloneTreeIsolatesMutableMetadata(t *testing.T) {
	child := &oci.Node{Type: oci.TypeFile, Ref: "a:1:4", Size: 4, Target: "original", Xattrs: map[string][]byte{"key": []byte("value")}}
	root := &oci.Node{Type: oci.TypeDir, Children: map[string]*oci.Node{"file": child, "shared": child}}
	cloned := CloneTree(root)
	if cloned == root || cloned.Children["file"] == child || cloned.Children["file"] != cloned.Children["shared"] {
		t.Fatal("metadata clone did not preserve independent shared identity")
	}
	copy := cloned.Children["file"]
	copy.Xattrs["key"][0] = 'X'
	copy.Target = "new"
	cloned.Children["added"] = &oci.Node{}
	if string(child.Xattrs["key"]) != "value" || child.Target != "original" || root.Children["added"] != nil || copy.Ref != child.Ref {
		t.Fatal("clone changed base metadata or blob ref")
	}
}

func TestOverlayObservationAbsentAndZeroLayers(t *testing.T) {
	root := &oci.Node{Type: oci.TypeDir, Children: map[string]*oci.Node{}}
	base := &oci.Manifest{}
	applied, m, observed, err := GraftLiveOverlayObserved(root, nil, nil, "external/image:latest", base, GraftOptions{})
	if applied || m != nil || err != nil || observed.Status != "absent" || observed.LayersApplied {
		t.Fatalf("absent observation: %+v %v", observed, err)
	}
	body := `{"schemaVersion":2,"layers":[]}`
	c := oci.NewClient("https://registry.invalid")
	c.HTTP = &http.Client{Transport: observationTransport(func(r *http.Request) (*http.Response, error) {
		response := body
		if r.URL.Path == "/token" {
			response = `{"token":"fixture"}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	applied, m, observed, err = GraftLiveOverlayObserved(root, nil, c, "tuna-os/yellowfin:kde", base, GraftOptions{})
	if applied || m != nil || err == nil || observed != nil || !strings.Contains(err.Error(), "overlay added no layers") {
		t.Fatalf("zero-layer overlay must retain existing rejection without evidence: %+v %v", observed, err)
	}
	_, _, observed, err = GraftLiveOverlayObserved(root, nil, c, "tuna-os/yellowfin:kde", base, GraftOptions{ExpectedOverlayDigest: "sha256:wrong"})
	if err == nil || observed != nil {
		t.Fatalf("mismatch must not return observation: %+v %v", observed, err)
	}
}

func TestObservedOverlayBaseRequiresPresentMatchingAnnotation(t *testing.T) {
	for _, tc := range []struct {
		name, annotation  string
		matched, mismatch bool
	}{
		{"absent", "", false, false},
		{"matching", "sha256:base", true, false},
		{"mismatch", "sha256:other", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			overlay := &oci.Manifest{Annotations: map[string]string{LiveOverlayActualBaseDigestAnnotation: tc.annotation}}
			matched, err := VerifyObservedOverlayBase(&oci.Manifest{}, overlay, "sha256:base")
			if matched != tc.matched || (err != nil) != tc.mismatch {
				t.Fatalf("matched=%v err=%v", matched, err)
			}
		})
	}
}

func TestOverlayApplyFailureHasNoObservation(t *testing.T) {
	root := &oci.Node{Type: oci.TypeDir, Children: map[string]*oci.Node{}}
	c := oci.NewClient("https://registry.invalid")
	c.HTTP = &http.Client{Transport: observationTransport(func(r *http.Request) (*http.Response, error) {
		status := 200
		response := `{"schemaVersion":2,"layers":[{"digest":"sha256:bad","mediaType":"application/vnd.oci.image.layer.v1.tar"}]}`
		if r.URL.Path == "/token" {
			response = `{"token":"fixture"}`
		}
		if strings.Contains(r.URL.Path, "/blobs/") {
			status = 404
			response = "missing"
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	applied, m, observed, err := GraftLiveOverlayObserved(root, nil, c, "tuna-os/yellowfin:kde", &oci.Manifest{}, GraftOptions{})
	if err == nil || applied || m != nil || observed != nil {
		t.Fatalf("failed apply returned evidence: applied=%v manifest=%v observed=%+v err=%v", applied, m, observed, err)
	}
}
