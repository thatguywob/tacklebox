package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tuna-os/tacklebox/internal/runner"
)

// EFI architecture suffixes probed, with the OCI architecture each serves
// and the removable-media name firmware loads for it. Tried in order when
// the image architecture is unknown, so an image carrying both takes x64.
var bootloaderArches = []struct{ ociArch, suffix, bootName string }{
	{"amd64", "x64", "BOOTX64.EFI"},
	{"arm64", "aa64", "BOOTAA64.EFI"},
}

// The directory systemd-boot lives in.
const sdbootDir = "usr/lib/systemd/boot/efi"

// hostRoot is the build host's filesystem root, probed by the fallback.
// A variable so tests can point it at a fixture tree.
var hostRoot = "/"

// copySdBootScript copies sdbootDir from $ROOT to $DEST, preserving the path.
func copySdBootScript() string {
	return fmt.Sprintf(`set -eu
if [ -e "$ROOT"/%[1]s ]; then mkdir -p "$DEST"/%[2]s; cp -a "$ROOT"/%[1]s "$DEST"/%[1]s; fi
`, shellEsc(sdbootDir), shellEsc(filepath.Dir(sdbootDir)))
}

// resolveSdBoot finds systemd-boot in a tree and returns its path with the
// name it takes on the ESP, or "" when the tree ships none. ociArch, when
// non-empty, restricts the search to that architecture's binary so a
// mismatched one is never staged. The .signed binary wins when a tree ships
// both: Debian's carries an Authenticode signature from the Debian Secure
// Boot CA.
func resolveSdBoot(root, ociArch string) (src, bootName string) {
	for _, arch := range bootloaderArches {
		if ociArch != "" && arch.ociArch != ociArch {
			continue
		}
		for _, n := range []string{
			"systemd-boot" + arch.suffix + ".efi.signed",
			"systemd-boot" + arch.suffix + ".efi",
		} {
			if p := filepath.Join(root, sdbootDir, n); isFile(p) {
				return p, arch.bootName
			}
		}
	}
	return "", ""
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// imageArch returns the OCI architecture of image ("amd64", "arm64"), or ""
// when it cannot be inspected.
func imageArch(image string) string {
	podman := UserPodmanPrefix()
	args := append(podman[1:], "image", "inspect", "--format", "{{.Architecture}}", image)
	out, err := runner.Output(podman[0], args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// StageBootloader copies systemd-boot into destDir, the EFI/BOOT
// directory of the installer media's ESP, and returns the name it wrote.
// It probes the image first, falling back to the build host if the image
// ships none. Both probes only accept the image's own architecture, so a
// cross-arch build never stages a binary the target firmware cannot load.
func StageBootloader(image, destDir string) (string, error) {
	if err := runner.Run("sudo", "mkdir", "-p", destDir); err != nil {
		return "", err
	}
	arch := imageArch(image)

	copied, err := runScriptOnImageMount(image, copySdBootScript())
	if err != nil {
		fmt.Printf(">>> [bootloader] WARNING: could not probe %s for systemd-boot (%v); trying the build host\n", image, err)
	} else {
		defer os.RemoveAll(copied)
		if src, bootName := resolveSdBoot(copied, arch); src != "" {
			if err := runner.Run("sudo", "cp", src, filepath.Join(destDir, bootName)); err != nil {
				return "", fmt.Errorf("place %s: %w", bootName, err)
			}
			fmt.Printf(">>> [bootloader] staged %s's systemd-boot as %s\n", image, bootName)
			return bootName, nil
		}
	}

	if src, bootName := resolveSdBoot(hostRoot, arch); src != "" {
		if err := runner.Run("sudo", "cp", src, filepath.Join(destDir, bootName)); err != nil {
			return "", fmt.Errorf("copy host EFI binary %s: %w", src, err)
		}
		fmt.Printf(">>> [bootloader] WARNING: %s ships no systemd-boot; staged the build host's as %s\n", image, bootName)
		return bootName, nil
	}

	archNote := ""
	if arch != "" {
		archNote = " for " + arch
	}
	return "", fmt.Errorf("no systemd-boot EFI binary%s in image %s or on the host; install systemd-boot-efi or systemd-boot-unsigned", archNote, image)
}
