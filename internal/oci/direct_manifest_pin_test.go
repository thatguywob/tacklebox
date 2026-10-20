package oci

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type directManifestTransport func(*http.Request) (*http.Response, error)

func (f directManifestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// A tag can resolve directly to a platform manifest, without an index.
// Its actual bytes must still satisfy the caller's expected digest.
func TestDirectManifestPinEnforced(t *testing.T) {
	body := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:config"},"layers":[]}`
	sum := sha256.Sum256([]byte(body))
	actual := "sha256:" + hex.EncodeToString(sum[:])
	wrong := "sha256:" + strings.Repeat("0", 64)
	for _, shape := range []string{"direct tag", "direct digest", "indexed"} {
		t.Run(shape, func(t *testing.T) {
			for _, tc := range []struct {
				name, pin string
				mismatch  bool
			}{
				{"unpinned", "", false},
				{"matching", actual, false},
				{"matching with whitespace", " " + actual + " ", false},
				{"mismatching", wrong, true},
				{"malformed", "not-a-digest", true},
				{"short digest", "sha256:abc", true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ref := Ref{Repo: "test/image", Tag: "latest"}
					if shape == "direct digest" {
						ref.Tag = actual
					}
					manifestRequests := 0
					client := NewClient("https://registry.invalid")
					client.HTTP = &http.Client{Transport: directManifestTransport(func(r *http.Request) (*http.Response, error) {
						response := body
						if r.URL.Path == "/token" {
							response = `{"token":"fixture"}`
						} else {
							manifestRequests++
							switch r.URL.Path {
							case "/v2/test/image/manifests/latest":
								if shape == "indexed" {
									response = fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":%q,"platform":{"architecture":"arm64","variant":"v8"}}]}`, actual)
								}
							case "/v2/test/image/manifests/" + actual:
							default:
								t.Fatalf("unexpected request: %s", r.URL.Path)
							}
						}
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
					})}
					manifest, digest, err := client.ResolveManifestForPlatformWithDigest(ref, "linux/arm64", tc.pin)
					if !tc.mismatch {
						if err != nil || manifest == nil || digest != actual {
							t.Fatalf("manifest=%v digest=%q err=%v", manifest, digest, err)
						}
						return
					}
					if manifestRequests != 1 {
						t.Fatalf("mismatch fetched %d manifests; want only top-level", manifestRequests)
					}
					var mismatch *ManifestDigestMismatchError
					if !errors.As(err, &mismatch) || !errors.Is(err, ErrManifestDigestMismatch) {
						t.Fatalf("wrong direct-manifest pin must fail with typed digest error; got manifest=%v digest=%q err=%v", manifest, digest, err)
					}
					if manifest != nil || digest != "" || mismatch.Want != strings.TrimSpace(tc.pin) || mismatch.Got != actual {
						t.Fatalf("invalid failure result: manifest=%v digest=%q mismatch=%+v", manifest, digest, mismatch)
					}
				})
			}
		})
	}
}
