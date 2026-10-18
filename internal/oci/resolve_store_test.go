package oci

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- ResolveManifest ---

func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func TestResolveManifestDirectImageManifest(t *testing.T) {
	// No index indirection: the tag resolves straight to an image manifest.
	m := &Manifest{MediaType: "application/vnd.oci.image.manifest.v1+json"}
	body, _ := json.Marshal(m)

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"tok"}`)
	})
	mux.HandleFunc("/v2/test/img/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.ResolveManifest(Ref{Repo: "test/img", Tag: "latest"}, "amd64")
	if err != nil {
		t.Fatalf("ResolveManifest: %v", err)
	}
	if len(got.Manifests) != 0 {
		t.Errorf("expected a direct image manifest with no nested Manifests, got %+v", got)
	}
}

func TestResolveManifestFollowsIndexToMatchingArch(t *testing.T) {
	amd64Manifest := &Manifest{MediaType: "application/vnd.oci.image.manifest.v1+json", Config: Descriptor{Digest: "sha256:amd64cfg"}}
	amd64Body, _ := json.Marshal(amd64Manifest)
	arm64Manifest := &Manifest{MediaType: "application/vnd.oci.image.manifest.v1+json", Config: Descriptor{Digest: "sha256:arm64cfg"}}
	arm64Body, _ := json.Marshal(arm64Manifest)

	amd64Digest := digestOf(amd64Body)
	arm64Digest := digestOf(arm64Body)

	index := &Manifest{
		MediaType: "application/vnd.oci.image.index.v1+json",
		Manifests: []Descriptor{
			{Digest: arm64Digest, Platform: &struct {
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			}{Architecture: "arm64"}},
			{Digest: amd64Digest, Platform: &struct {
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			}{Architecture: "amd64"}},
		},
	}
	indexBody, _ := json.Marshal(index)

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"tok"}`)
	})
	mux.HandleFunc("/v2/test/img/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Write(indexBody)
	})
	mux.HandleFunc("/v2/test/img/manifests/"+amd64Digest, func(w http.ResponseWriter, r *http.Request) {
		w.Write(amd64Body)
	})
	mux.HandleFunc("/v2/test/img/manifests/"+arm64Digest, func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not fetch the arm64 platform manifest when amd64 was requested")
		w.Write(arm64Body)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.ResolveManifest(Ref{Repo: "test/img", Tag: "latest"}, "amd64")
	if err != nil {
		t.Fatalf("ResolveManifest: %v", err)
	}
	if got.Config.Digest != "sha256:amd64cfg" {
		t.Errorf("resolved the wrong platform manifest: got config digest %s", got.Config.Digest)
	}
}

func TestResolveManifestFallsBackToFirstWhenArchMissing(t *testing.T) {
	onlyManifest := &Manifest{MediaType: "application/vnd.oci.image.manifest.v1+json", Config: Descriptor{Digest: "sha256:onlycfg"}}
	onlyBody, _ := json.Marshal(onlyManifest)
	onlyDigest := digestOf(onlyBody)

	index := &Manifest{
		MediaType: "application/vnd.oci.image.index.v1+json",
		Manifests: []Descriptor{
			{Digest: onlyDigest, Platform: &struct {
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			}{Architecture: "riscv64"}},
		},
	}
	indexBody, _ := json.Marshal(index)

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"tok"}`)
	})
	mux.HandleFunc("/v2/test/img/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Write(indexBody)
	})
	mux.HandleFunc("/v2/test/img/manifests/"+onlyDigest, func(w http.ResponseWriter, r *http.Request) {
		w.Write(onlyBody)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.ResolveManifest(Ref{Repo: "test/img", Tag: "latest"}, "amd64")
	if err != nil {
		t.Fatalf("ResolveManifest: %v", err)
	}
	if got.Config.Digest != "sha256:onlycfg" {
		t.Errorf("expected fallback to the only listed manifest, got config digest %s", got.Config.Digest)
	}
}

func TestResolveManifestTokenFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.ResolveManifest(Ref{Repo: "test/img", Tag: "latest"}, "amd64")
	if err == nil {
		t.Fatal("expected an error when the token endpoint refuses")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveManifestNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"tok"}`)
	})
	mux.HandleFunc("/v2/test/img/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.ResolveManifest(Ref{Repo: "test/img", Tag: "latest"}, "amd64")
	if err == nil {
		t.Fatal("expected an error for a 404 manifest")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveManifestRejectsSubstitutedPlatformManifest(t *testing.T) {
	// The index names one platform manifest by digest; the server answers
	// that request with a different document, as a compromised registry
	// mirror or CORS shim would. Its layer digests must never be trusted:
	// blob verification checks blobs against exactly these numbers.
	honest := &Manifest{MediaType: "application/vnd.oci.image.manifest.v1+json", Config: Descriptor{Digest: "sha256:honestcfg"}}
	honestBody, _ := json.Marshal(honest)
	honestDigest := digestOf(honestBody)

	forged := &Manifest{MediaType: "application/vnd.oci.image.manifest.v1+json", Config: Descriptor{Digest: "sha256:forgedcfg"}}
	forgedBody, _ := json.Marshal(forged)

	index := &Manifest{
		MediaType: "application/vnd.oci.image.index.v1+json",
		Manifests: []Descriptor{
			{Digest: honestDigest, Platform: &struct {
				Architecture string `json:"architecture"`
				Variant      string `json:"variant"`
			}{Architecture: "amd64"}},
		},
	}
	indexBody, _ := json.Marshal(index)

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"tok"}`)
	})
	mux.HandleFunc("/v2/test/img/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Write(indexBody)
	})
	mux.HandleFunc("/v2/test/img/manifests/"+honestDigest, func(w http.ResponseWriter, r *http.Request) {
		w.Write(forgedBody)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.ResolveManifest(Ref{Repo: "test/img", Tag: "latest"}, "amd64")
	if err == nil {
		t.Fatalf("expected a digest mismatch, got manifest with config %s", got.Config.Digest)
	}
	if !strings.Contains(err.Error(), "digest mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveManifestBadJSONDecode(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"tok"}`)
	})
	mux.HandleFunc("/v2/test/img/manifests/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `not json`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.ResolveManifest(Ref{Repo: "test/img", Tag: "latest"}, "amd64")
	if err == nil {
		t.Fatal("expected a decode error for malformed manifest JSON")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- UnpackOnto ---

func TestUnpackOntoAppliesOnlyDeltaLayers(t *testing.T) {
	base := zstdLayer(t, []tarEntry{
		{hdr: tar.Header{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "etc/base.txt", Typeflag: tar.TypeReg}, body: []byte("base\n")},
	})
	delta := zstdLayer(t, []tarEntry{
		{hdr: tar.Header{Name: "etc/extra.txt", Typeflag: tar.TypeReg}, body: []byte("extra\n")},
	})

	srv, m := fakeRegistry(t, [][]byte{base, delta})
	defer srv.Close()
	c := NewClient(srv.URL)
	ref := Ref{Repo: "test/img", Tag: "latest"}
	store := &MemStore{}

	// Unpack the base alone first (only the base layer), to build the
	// starting tree, then UnpackOnto with the base's digest skipped —
	// mirroring how a real overlay/delta consumer works.
	baseManifest := &Manifest{MediaType: m.MediaType, Config: m.Config, Layers: m.Layers[:1]}
	root, err := c.Unpack(ref, baseManifest, store, nil)
	if err != nil {
		t.Fatalf("Unpack (base): %v", err)
	}

	skip := map[string]bool{m.Layers[0].Digest: true}
	if err := c.UnpackOnto(root, ref, m, store, skip, nil); err != nil {
		t.Fatalf("UnpackOnto: %v", err)
	}

	if root.Lookup("etc/base.txt") == nil {
		t.Error("expected the base tree to be preserved")
	}
	if root.Lookup("etc/extra.txt") == nil {
		t.Error("expected the delta layer's file to be applied onto the base tree")
	}
}

func TestUnpackOntoErrorsWhenNothingBeyondSkip(t *testing.T) {
	base := zstdLayer(t, []tarEntry{
		{hdr: tar.Header{Name: "etc/base.txt", Typeflag: tar.TypeReg}, body: []byte("base\n")},
	})
	srv, m := fakeRegistry(t, [][]byte{base})
	defer srv.Close()
	c := NewClient(srv.URL)
	ref := Ref{Repo: "test/img", Tag: "latest"}
	store := &MemStore{}

	root := &Node{Type: TypeDir, Mode: 0o755, Children: map[string]*Node{}}
	skip := map[string]bool{m.Layers[0].Digest: true}
	err := c.UnpackOnto(root, ref, m, store, skip, nil)
	if err == nil {
		t.Fatal("expected an error when every layer is skipped (overlay adds nothing)")
	}
	if !strings.Contains(err.Error(), "no layers beyond the base") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUnpackOntoPropagatesBlobFailure(t *testing.T) {
	base := zstdLayer(t, []tarEntry{{hdr: tar.Header{Name: "a", Typeflag: tar.TypeReg}, body: []byte("a")}})
	srv, m := fakeRegistry(t, [][]byte{base})
	defer srv.Close()
	c := NewClient(srv.URL)
	ref := Ref{Repo: "test/img", Tag: "latest"}
	store := &MemStore{}

	// Corrupt the digest so the registry 404s on the blob fetch.
	m.Layers[0].Digest = "sha256:doesnotexist"
	root := &Node{Type: TypeDir, Mode: 0o755, Children: map[string]*Node{}}
	err := c.UnpackOnto(root, ref, m, store, nil, nil)
	if err == nil {
		t.Fatal("expected the missing blob to produce an error")
	}
	if !strings.Contains(err.Error(), "overlay layer 0") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- DirStore ---

func TestDirStorePutOpenRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blobs")
	s := &DirStore{Dir: dir}

	ref1, size1, err := s.Put(strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if size1 != 5 {
		t.Errorf("size = %d, want 5", size1)
	}
	ref2, _, err := s.Put(strings.NewReader("world!"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref1 == ref2 {
		t.Error("expected distinct content-addressed names for successive Puts")
	}

	r, err := s.Open(ref1)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if string(got) != "hello" {
		t.Errorf("Open(ref1) = %q, want hello", got)
	}
}

func TestDirStorePutCreatesDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "blobs")
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("dir should not exist yet")
	}
	s := &DirStore{Dir: dir}
	if _, _, err := s.Put(strings.NewReader("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("expected Put to create the store dir: %v", err)
	}
}

// --- ApplyTar ---

func TestApplyTarBuildsTreeFromUncompressedStream(t *testing.T) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	entries := []tarEntry{
		{hdr: tar.Header{Name: "usr/bin/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "usr/bin/tool", Typeflag: tar.TypeReg, Mode: 0o755}, body: []byte("#!/bin/sh\n")},
	}
	for _, e := range entries {
		h := e.hdr
		h.Size = int64(len(e.body))
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	store := &MemStore{}
	root, err := ApplyTar(&raw, store)
	if err != nil {
		t.Fatalf("ApplyTar: %v", err)
	}
	n := root.Lookup("usr/bin/tool")
	if n == nil || n.Type != TypeFile {
		t.Fatalf("expected usr/bin/tool to be a file node, got %+v", n)
	}
	r, err := store.Open(n.Ref)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if string(got) != "#!/bin/sh\n" {
		t.Errorf("content = %q", got)
	}
}

func TestApplyTarPropagatesMalformedStream(t *testing.T) {
	_, err := ApplyTar(strings.NewReader("not a tar stream at all"), &MemStore{})
	if err == nil {
		t.Fatal("expected an error for a malformed tar stream")
	}
}

// --- ResolveManifestForPlatform (exact platform selection) ---

// platformDesc builds an index descriptor for one architecture/variant.
func platformDesc(arch, variant, digest string) Descriptor {
	d := Descriptor{Digest: digest}
	d.Platform = &struct {
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	}{Architecture: arch, Variant: variant}
	return d
}

// platformManifestBody builds a minimal platform manifest whose config digest
// names it, returning the body and its digest.
func platformManifestBody(t *testing.T, cfg string) (body []byte, digest string) {
	t.Helper()
	m := &Manifest{MediaType: "application/vnd.oci.image.manifest.v1+json", Config: Descriptor{Digest: cfg}}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b, digestOf(b)
}

// multiArchServer serves tagPath with indexBody and each platform body at
// manifests/<digest>, recording every platform-manifest fetch so tests can
// assert exactly one descriptor was selected.
func multiArchServer(t *testing.T, tagPath string, indexBody []byte, bodies map[string][]byte, fetched *[]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"tok"}`)
	})
	mux.HandleFunc("/v2/test/img/manifests/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v2/test/img/manifests/")
		if rest == tagPath {
			w.Write(indexBody)
			return
		}
		b, ok := bodies[rest]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		*fetched = append(*fetched, rest)
		w.Write(b)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// Requesting x86_64, x86_64_v2, or ARM64 must select only the matching
// descriptor — and fetch only that platform manifest, never a substitute.
func TestResolveManifestForPlatformSelectsExactArch(t *testing.T) {
	amdBody, amdDigest := platformManifestBody(t, "sha256:amd64cfg")
	v2Body, v2Digest := platformManifestBody(t, "sha256:v2cfg")
	armBody, armDigest := platformManifestBody(t, "sha256:arm64cfg")
	index := &Manifest{
		MediaType: "application/vnd.oci.image.index.v1+json",
		Manifests: []Descriptor{
			platformDesc("amd64", "", amdDigest),
			platformDesc("amd64", "v2", v2Digest),
			platformDesc("arm64", "", armDigest),
		},
	}
	indexBody, _ := json.Marshal(index)
	bodies := map[string][]byte{amdDigest: amdBody, v2Digest: v2Body, armDigest: armBody}

	cases := []struct {
		platform   string
		wantDigest string
		wantCfg    string
	}{
		{"linux/amd64", amdDigest, "sha256:amd64cfg"},
		{"amd64", amdDigest, "sha256:amd64cfg"},
		{"x86_64", amdDigest, "sha256:amd64cfg"},
		{"linux/amd64/v2", v2Digest, "sha256:v2cfg"},
		{"linux/arm64", armDigest, "sha256:arm64cfg"},
		{"linux/arm64/v8", armDigest, "sha256:arm64cfg"},
		{"arm64", armDigest, "sha256:arm64cfg"},
		{"aarch64", armDigest, "sha256:arm64cfg"},
	}
	for _, tc := range cases {
		t.Run(tc.platform, func(t *testing.T) {
			var fetched []string
			srv := multiArchServer(t, "latest", indexBody, bodies, &fetched)
			c := NewClient(srv.URL)
			got, digest, err := c.ResolveManifestForPlatform(Ref{Repo: "test/img", Tag: "latest"}, tc.platform)
			if err != nil {
				t.Fatalf("ResolveManifestForPlatform(%q): %v", tc.platform, err)
			}
			if digest != tc.wantDigest {
				t.Errorf("digest = %s, want %s", digest, tc.wantDigest)
			}
			if got.Config.Digest != tc.wantCfg {
				t.Errorf("resolved the wrong platform manifest: config = %s, want %s", got.Config.Digest, tc.wantCfg)
			}
			if len(fetched) != 1 || fetched[0] != tc.wantDigest {
				t.Errorf("fetched platform manifests %v, want exactly [%s]", fetched, tc.wantDigest)
			}
		})
	}
}

// An ARM64 request against an amd64-only index must fail closed with a typed
// error — never silently fall back to Manifests[0].
func TestResolveManifestForPlatformMissingPlatformFailsClosed(t *testing.T) {
	amdBody, amdDigest := platformManifestBody(t, "sha256:amd64cfg")
	v2Body, v2Digest := platformManifestBody(t, "sha256:v2cfg")
	index := &Manifest{
		MediaType: "application/vnd.oci.image.index.v1+json",
		Manifests: []Descriptor{
			platformDesc("amd64", "", amdDigest),
			platformDesc("amd64", "v2", v2Digest),
		},
	}
	indexBody, _ := json.Marshal(index)
	bodies := map[string][]byte{amdDigest: amdBody, v2Digest: v2Body}

	t.Run("arm64-absent", func(t *testing.T) {
		var fetched []string
		srv := multiArchServer(t, "latest", indexBody, bodies, &fetched)
		c := NewClient(srv.URL)
		_, _, err := c.ResolveManifestForPlatform(Ref{Repo: "test/img", Tag: "latest"}, "linux/arm64")
		if err == nil {
			t.Fatal("expected a platform-not-found error, got nil (silent Manifests[0] fallback?)")
		}
		var notFound *PlatformNotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("error type = %T, want *PlatformNotFoundError: %v", err, err)
		}
		if !errors.Is(err, ErrPlatformNotFound) {
			t.Errorf("error does not wrap ErrPlatformNotFound: %v", err)
		}
		if notFound.Platform != "linux/arm64" {
			t.Errorf("requested platform = %q, want linux/arm64", notFound.Platform)
		}
		if len(notFound.Available) != 2 {
			t.Errorf("available = %v, want the two indexed amd64 descriptors", notFound.Available)
		}
		if len(fetched) != 0 {
			t.Errorf("must not fetch any platform manifest on a miss, fetched %v", fetched)
		}
	})

	t.Run("v2-never-matches-plain-amd64", func(t *testing.T) {
		plainBody, plainDigest := platformManifestBody(t, "sha256:plaincfg")
		plain := &Manifest{
			MediaType: "application/vnd.oci.image.index.v1+json",
			Manifests: []Descriptor{platformDesc("amd64", "", plainDigest)},
		}
		plainIndex, _ := json.Marshal(plain)
		var fetched []string
		srv := multiArchServer(t, "latest", plainIndex, map[string][]byte{plainDigest: plainBody}, &fetched)
		c := NewClient(srv.URL)
		_, _, err := c.ResolveManifestForPlatform(Ref{Repo: "test/img", Tag: "latest"}, "linux/amd64/v2")
		var notFound *PlatformNotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("a variantless amd64 entry must not satisfy linux/amd64/v2, got err=%v", err)
		}
	})
}

// The expected (pinned) digest is compared against the MATCHING descriptor's
// digest — never the index digest — and a mismatch fails before any fetch.
func TestResolveManifestForPlatformExpectedDigestEnforced(t *testing.T) {
	amdBody, amdDigest := platformManifestBody(t, "sha256:amd64cfg")
	armBody, armDigest := platformManifestBody(t, "sha256:arm64cfg")
	index := &Manifest{
		MediaType: "application/vnd.oci.image.index.v1+json",
		Manifests: []Descriptor{
			platformDesc("amd64", "", amdDigest),
			platformDesc("arm64", "", armDigest),
		},
	}
	indexBody, _ := json.Marshal(index)
	indexDigest := digestOf(indexBody)
	bodies := map[string][]byte{amdDigest: amdBody, armDigest: armBody}

	var fetched []string
	srv := multiArchServer(t, "latest", indexBody, bodies, &fetched)
	c := NewClient(srv.URL)
	ref := Ref{Repo: "test/img", Tag: "latest"}

	if _, digest, err := c.ResolveManifestForPlatformWithDigest(ref, "linux/amd64", amdDigest); err != nil || digest != amdDigest {
		t.Errorf("matching pin should resolve: digest=%s err=%v", digest, err)
	}
	if len(fetched) != 1 {
		t.Fatalf("expected one platform fetch for the matching pin, got %v", fetched)
	}

	_, _, err := c.ResolveManifestForPlatformWithDigest(ref, "linux/amd64", "sha256:doesnotmatch")
	var mismatch *ManifestDigestMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("wrong pin must fail typed, got %v", err)
	}
	if !errors.Is(err, ErrManifestDigestMismatch) {
		t.Errorf("error does not wrap ErrManifestDigestMismatch: %v", err)
	}
	if mismatch.Got != amdDigest || mismatch.Want != "sha256:doesnotmatch" {
		t.Errorf("mismatch = got %s want %s, expected descriptor digest vs pin", mismatch.Got, mismatch.Want)
	}

	// The index digest must not satisfy the pin: platform and index digests
	// are never conflated.
	_, _, err = c.ResolveManifestForPlatformWithDigest(ref, "linux/amd64", indexDigest)
	if !errors.As(err, &mismatch) {
		t.Fatalf("index digest as pin must fail typed, got %v", err)
	}
}

// A registry that answers the by-digest platform request with different
// bytes is rejected with a typed digest error.
func TestResolveManifestForPlatformRejectsSubstitutedBody(t *testing.T) {
	honestBody, honestDigest := platformManifestBody(t, "sha256:honestcfg")
	_ = honestBody
	forgedBody, _ := platformManifestBody(t, "sha256:forgedcfg")
	index := &Manifest{
		MediaType: "application/vnd.oci.image.index.v1+json",
		Manifests: []Descriptor{platformDesc("arm64", "", honestDigest)},
	}
	indexBody, _ := json.Marshal(index)
	var fetched []string
	srv := multiArchServer(t, "latest", indexBody, map[string][]byte{honestDigest: forgedBody}, &fetched)
	c := NewClient(srv.URL)
	_, _, err := c.ResolveManifestForPlatform(Ref{Repo: "test/img", Tag: "latest"}, "linux/arm64")
	var mismatch *ManifestDigestMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("substituted platform body must fail typed, got %v", err)
	}
	if mismatch.Want != honestDigest {
		t.Errorf("want = %s, expected the descriptor digest %s", mismatch.Want, honestDigest)
	}
}

// --- Blob digest verification through stream completion and close ---

func TestBlobVerifiesDigestThroughClose(t *testing.T) {
	payload := []byte("live-payload-bytes")
	good := digestOf(payload)
	otherDigest := digestOf([]byte("something-else-entirely"))

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"tok"}`)
	})
	mux.HandleFunc("/v2/test/img/blobs/", func(w http.ResponseWriter, r *http.Request) {
		// Serve the same bytes under both digests: the second fetch must
		// then fail verification at Close, proving the hash spans the
		// whole stream rather than the request path.
		w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := NewClient(srv.URL)
	ref := Ref{Repo: "test/img", Tag: "latest"}

	rc, err := c.Blob(ref, Descriptor{Digest: good})
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Errorf("Close on a fully-consumed matching stream should succeed: %v", err)
	}

	rc, err = c.Blob(ref, Descriptor{Digest: otherDigest})
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := rc.Close(); err == nil {
		t.Fatal("expected a digest mismatch at Close for corrupted bytes")
	} else if !strings.Contains(err.Error(), "digest mismatch") {
		t.Errorf("unexpected error: %v", err)
	}

	// Abandoning the stream early asserts nothing: Close stays silent.
	rc, err = c.Blob(ref, Descriptor{Digest: otherDigest})
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := rc.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Errorf("Close on a partial stream should not assert: %v", err)
	}
}
