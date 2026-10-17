package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuna-os/tacklebox/internal/purefs"
)

func TestSplitRef(t *testing.T) {
	cases := []struct {
		in       string
		wantRepo string
		wantTag  string
		wantOK   bool
	}{
		{"ghcr.io/tuna-os/tunaos:latest", "ghcr.io/tuna-os/tunaos", "latest", true},
		{"localhost:5000/foo:bar", "localhost:5000/foo", "bar", true},
		{"foo/bar", "", "", false},
		{"foo:", "", "", false},
		{":tag", "", "", false},
		{"", "", "", false},
		{"localhost:5000/foo", "", "", false},
	}
	for _, c := range cases {
		repo, tag, ok := splitRef(c.in)
		if ok != c.wantOK || repo != c.wantRepo || tag != c.wantTag {
			t.Errorf("splitRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, repo, tag, ok, c.wantRepo, c.wantTag, c.wantOK)
		}
	}
}

func TestArenaRefRoundTrip(t *testing.T) {
	cases := []struct {
		prefix string
		off    int64
		length int64
	}{
		{"a", 0, 0},
		{"o", 4096, 1048576},
		{"d", 0, 5_368_709_120}, // > 4 GiB, exercises int64 not int
	}
	for _, c := range cases {
		ref := formatArenaRef(c.prefix, c.off, c.length)
		prefix, off, length, err := parseArenaRef(ref)
		if err != nil {
			t.Fatalf("parseArenaRef(%q) unexpected error: %v", ref, err)
		}
		if prefix != c.prefix || off != c.off || length != c.length {
			t.Errorf("parseArenaRef(%q) = (%q, %d, %d), want (%q, %d, %d)",
				ref, prefix, off, length, c.prefix, c.off, c.length)
		}
	}
}

func TestParseArenaRefRejectsMalformed(t *testing.T) {
	for _, ref := range []string{
		"",
		"a",
		"a:1",
		"a:1:2:3",
		"a:x:2",
		"a:1:x",
	} {
		if _, _, _, err := parseArenaRef(ref); err == nil {
			t.Errorf("parseArenaRef(%q) = nil error, want error", ref)
		}
	}
}

func TestBuildRequestHostHelpers(t *testing.T) {
	const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	req := &BuildRequest{
		SchemaVersion: purefs.BuildRequestSchemaVersion,
		Kind:          purefs.BuildRequestKind,
		Image:         "ghcr.io/tuna-os/sailfin:kde",
		Target: &TargetIdentity{
			Variant:  "sailfin",
			Flavor:   "kde",
			Platform: "linux/amd64",
		},
		ResolvedInputs: &ResolvedInputs{
			PlatformManifestDigest: digest,
		},
		Options: BuildOptions{
			Label: "SAILFIN_KDE",
		},
		QualificationID: "qual-sailfin-kde",
	}
	raw, err := req.ExportJSON()
	if err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}
	parsed, err := ParseBuildRequest(raw)
	if err != nil {
		t.Fatalf("ParseBuildRequest: %v", err)
	}
	if !parsed.IsCuratedTarget() || parsed.IsCustomImage() {
		t.Errorf("IsCuratedTarget=%v IsCustomImage=%v, want true/false", parsed.IsCuratedTarget(), parsed.IsCustomImage())
	}
	if err := parsed.VerifyResolved("tuna-os/sailfin:kde", "amd64", digest); err != nil {
		t.Errorf("VerifyResolved failed on matching resolved state: %v", err)
	}
	if err := parsed.VerifyResolved("tuna-os/sailfin:kde", "arm64", digest); err == nil {
		t.Error("VerifyResolved succeeded on mismatched platform arm64, want error")
	}
	if err := parsed.VerifyResolved("tuna-os/sailfin:kde", "amd64", "sha256:2222222222222222222222222222222222222222222222222222222222222222"); err == nil {
		t.Error("VerifyResolved succeeded on mismatched digest, want error")
	}
}

func TestSinkWriterHostHelper_RejectsWriteFailureWithoutIncrementingWritten(t *testing.T) {
	sw := NewSinkWriter(func(chunk []byte) error {
		if string(chunk) == "fail" {
			return errors.New("stream closed")
		}
		return nil
	})
	if n, err := sw.Write([]byte("ok")); err != nil || n != 2 || sw.Written != 2 {
		t.Fatalf("Write(ok) = (%d, %v), Written=%d; want (2, nil, 2)", n, err, sw.Written)
	}
	if n, err := sw.Write([]byte("fail")); err == nil || !strings.Contains(err.Error(), "stream closed") || n != 0 || sw.Written != 2 {
		t.Fatalf("Write(fail) = (%d, %v), Written=%d; want (0, stream closed, 2)", n, err, sw.Written)
	}
}
