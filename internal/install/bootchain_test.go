package install

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/tacklebox/internal/runner"
)

// touch creates rel under root, holding a stub PE header.
func touch(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("PE\x00"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveSdBoot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		files    []string
		wantName string
		wantSrc  string
	}{
		{
			name:     "systemd-boot from the image, not the host",
			files:    []string{"usr/lib/systemd/boot/efi/systemd-bootx64.efi"},
			wantName: "BOOTX64.EFI",
			wantSrc:  "systemd-bootx64.efi",
		},
		{
			name:     "aarch64 systemd-boot lands as BOOTAA64.EFI",
			files:    []string{"usr/lib/systemd/boot/efi/systemd-bootaa64.efi"},
			wantName: "BOOTAA64.EFI",
			wantSrc:  "systemd-bootaa64.efi",
		},
		{
			// Debian installs systemd-boot-efi-amd64-signed alongside the
			// unsigned binary; the signed one is the one to stage.
			name: "the .signed binary wins over the unsigned one",
			files: []string{
				"usr/lib/systemd/boot/efi/systemd-bootx64.efi",
				"usr/lib/systemd/boot/efi/systemd-bootx64.efi.signed",
			},
			wantName: "BOOTX64.EFI",
			wantSrc:  "systemd-bootx64.efi.signed",
		},
		{
			// x64 is tried first; the image's own architecture is not read.
			name: "an image carrying both architectures takes x64",
			files: []string{
				"usr/lib/systemd/boot/efi/systemd-bootx64.efi",
				"usr/lib/systemd/boot/efi/systemd-bootaa64.efi",
			},
			wantName: "BOOTX64.EFI",
			wantSrc:  "systemd-bootx64.efi",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, f := range tc.files {
				touch(t, root, f)
			}
			src, name := resolveSdBoot(root, "")
			if name != tc.wantName {
				t.Errorf("ESP name = %q, want %q", name, tc.wantName)
			}
			if !strings.HasSuffix(src, tc.wantSrc) {
				t.Errorf("staged from %q, want one ending %q", src, tc.wantSrc)
			}
		})
	}
}

// An image with no systemd-boot resolves to nothing rather than an error, so
// the caller can fall back to the host.
func TestResolveSdBootReportsNothingForAnImageWithout(t *testing.T) {
	if src, _ := resolveSdBoot(t.TempDir(), ""); src != "" {
		t.Errorf("resolved %q from an image with no systemd-boot", src)
	}
}

// The generated script must reach the directory resolveSdBoot looks in.
func TestCopyScriptCoversSdBootDir(t *testing.T) {
	if !strings.Contains(copySdBootScript(), sdbootDir) {
		t.Errorf("copy script does not mention %s", sdbootDir)
	}
}

// The image's architecture restricts the search: an arm64 image never gets
// the x64 binary, even when it is tried first.
func TestResolveSdBootHonoursImageArch(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "usr/lib/systemd/boot/efi/systemd-bootx64.efi")
	touch(t, root, "usr/lib/systemd/boot/efi/systemd-bootaa64.efi")
	if src, name := resolveSdBoot(root, "arm64"); name != "BOOTAA64.EFI" || !strings.HasSuffix(src, "systemd-bootaa64.efi") {
		t.Errorf("arm64: got %q from %q, want BOOTAA64.EFI from systemd-bootaa64.efi", name, src)
	}
	only := t.TempDir()
	touch(t, only, "usr/lib/systemd/boot/efi/systemd-bootx64.efi")
	if src, _ := resolveSdBoot(only, "arm64"); src != "" {
		t.Errorf("arm64 resolved mismatched binary %q", src)
	}
}

// stubStage fakes the podman calls StageBootloader makes: inspect reports
// arch, and the image mount either fails or yields imageFiles under $DEST.
// It returns the cp sources StageBootloader issued.
func stubStage(t *testing.T, arch string, mountErr error, imageFiles []string) *[]string {
	t.Helper()
	origRun, origOut, origHost := runner.RunFn, runner.OutputFn, hostRoot
	t.Cleanup(func() { runner.RunFn, runner.OutputFn, hostRoot = origRun, origOut, origHost })
	hostRoot = t.TempDir()

	runner.OutputFn = func(_ string, args ...string) ([]byte, error) {
		return []byte(arch + "\n"), nil
	}
	var copied []string
	runner.RunFn = func(_ io.Reader, name string, args ...string) error {
		if name == "sudo" && len(args) == 3 && args[0] == "cp" {
			copied = append(copied, args[1])
			return nil
		}
		for _, arg := range args {
			idx := strings.Index(arg, "DEST='")
			if idx == -1 {
				continue
			}
			if mountErr != nil {
				return mountErr
			}
			rest := arg[idx+6:]
			dest := rest[:strings.Index(rest, "'")]
			for _, f := range imageFiles {
				touch(t, dest, f)
			}
		}
		return nil
	}
	return &copied
}

func TestStageBootloader_FromImage(t *testing.T) {
	copied := stubStage(t, "amd64", nil, []string{"usr/lib/systemd/boot/efi/systemd-bootx64.efi"})
	bootName, err := StageBootloader("localhost/myimage:latest", t.TempDir())
	if err != nil {
		t.Fatalf("expected success staging from image, got: %v", err)
	}
	if bootName != "BOOTX64.EFI" {
		t.Fatalf("got bootName %q, want BOOTX64.EFI", bootName)
	}
	if len(*copied) != 1 || strings.HasPrefix((*copied)[0], hostRoot) {
		t.Fatalf("staged %v, want one binary from the image", *copied)
	}
}

// A failed image mount falls back to the host rather than aborting.
func TestStageBootloader_MountErrorFallsBackToHost(t *testing.T) {
	copied := stubStage(t, "amd64", errors.New("image not known"), nil)
	touch(t, hostRoot, "usr/lib/systemd/boot/efi/systemd-bootx64.efi")
	bootName, err := StageBootloader("localhost/myimage:latest", t.TempDir())
	if err != nil {
		t.Fatalf("expected host fallback, got: %v", err)
	}
	if bootName != "BOOTX64.EFI" || len(*copied) != 1 || !strings.HasPrefix((*copied)[0], hostRoot) {
		t.Fatalf("got %q from %v, want BOOTX64.EFI from the host", bootName, *copied)
	}
}

// An arm64 image built on an x86_64 host must not get the host's x64 binary.
func TestStageBootloader_RefusesMismatchedHostArch(t *testing.T) {
	copied := stubStage(t, "arm64", nil, nil)
	touch(t, hostRoot, "usr/lib/systemd/boot/efi/systemd-bootx64.efi")
	if bootName, err := StageBootloader("localhost/myimage:latest", t.TempDir()); err == nil {
		t.Fatalf("staged %q from %v for an arm64 image; want an error", bootName, *copied)
	}
}
