package purefs

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tuna-os/tacklebox/internal/oci"
)
// Annotation keys a live-overlay publisher stamps on its platform manifest
// recording which base platform manifest the delta was built against.
// Either key satisfies verification; the "actual-" spelling is preferred.
const (
	LiveOverlayActualBaseDigestAnnotation = "org.tunaos.live-overlay.actual-base-digest"
	LiveOverlayBaseDigestAnnotation       = "org.tunaos.live-overlay.base-digest"
)

// Overlay mismatch reasons carried by OverlayMismatchError.
const (
	// OverlayReasonMissing: the overlay is required (pinned by the caller)
	// but no overlay exists for the exact requested platform.
	OverlayReasonMissing = "missing-live-overlay"
	// OverlayReasonDigestMismatch: the resolved overlay platform manifest
	// digest differs from the caller-pinned overlay digest.
	OverlayReasonDigestMismatch = "overlay-digest-mismatch"
	// OverlayReasonBaseDigestMismatch: the overlay's base-digest
	// annotation differs from the selected base platform manifest digest.
	OverlayReasonBaseDigestMismatch = "overlay-base-digest-mismatch"
)

// ErrLiveOverlayMissing is the sentinel wrapped by OverlayMismatchError
// with Reason OverlayReasonMissing, so callers can errors.Is the blocker
// without matching text.
var ErrLiveOverlayMissing = errors.New("live overlay missing")

// OverlayMismatchError is the typed blocker returned when a required live
// overlay is missing or fails digest verification. Reason is one of the
// OverlayReason* constants; Want/Got carry the compared digests (or the
// requested platform and available architectures for a missing overlay).
type OverlayMismatchError struct {
	Image  string
	Reason string
	Want   string
	Got    string
}

func (e *OverlayMismatchError) Error() string {
	switch e.Reason {
	case OverlayReasonMissing:
		if e.Image != "" {
			return fmt.Sprintf("live overlay missing for image %q (platform %s, available: %s)", e.Image, e.Want, e.Got)
		}
		return "live overlay missing"
	case OverlayReasonDigestMismatch:
		return fmt.Sprintf("live overlay digest mismatch: got %s want %s", e.Got, e.Want)
	case OverlayReasonBaseDigestMismatch:
		return fmt.Sprintf("live overlay base digest mismatch: overlay built against %s, selected base is %s", e.Got, e.Want)
	default:
		return fmt.Sprintf("live overlay mismatch (%s)", e.Reason)
	}
}

// Is reports ErrLiveOverlayMissing for the missing-overlay reason so
// errors.Is(err, ErrLiveOverlayMissing) holds for required-but-absent
// overlays only — digest mismatches are NOT "missing".
func (e *OverlayMismatchError) Is(target error) bool {
	return target == ErrLiveOverlayMissing && e.Reason == OverlayReasonMissing
}

// GraftOptions tunes GraftLiveOverlayWithOptions.
type GraftOptions struct {
	// Platform is the exact requested platform ("linux/amd64",
	// "linux/amd64/v2", "linux/arm64", or an arch shorthand); "" means
	// "amd64".
	Platform string
	// ExpectedOverlayDigest pins the overlay's platform-manifest digest
	// (BuildRequest ResolvedInputs.LiveOverlayDigest). Non-empty both
	// requires the overlay's presence and refuses a digest mismatch
	// rather than grafting the wrong delta.
	ExpectedOverlayDigest string
	// Required treats a missing overlay as a blocker even without a
	// pinned digest.
	Required bool
	// Progress, if non-nil, is called as layers land.
	Progress func(layer, total int)
}

// LiveOverlayRepo is where the per-variant live customize delta is published.
// Its tags are "<variant>-<flavor>", e.g. yellowfin-kde.
const LiveOverlayRepo = "tuna-os/live-overlay"

// LiveOverlayRef maps a base image reference to its live-overlay counterpart.
//
// Only tuna-os images have one: the overlay is the *output* of that recipe's
// live_customize step. Returns ok=false for anything else, and for refs
// without both a repo path and a tag, since the tag is built from the two.
//
//	tuna-os/yellowfin:kde -> tuna-os/live-overlay:yellowfin-kde
func LiveOverlayRef(image string) (oci.Ref, bool) {
	if !strings.HasPrefix(image, "tuna-os/") {
		return oci.Ref{}, false
	}
	parts := strings.SplitN(strings.TrimPrefix(image, "tuna-os/"), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return oci.Ref{}, false
	}
	return oci.Ref{Repo: LiveOverlayRepo, Tag: parts[0] + "-" + parts[1]}, true
}

// GraftLiveOverlay applies the published live-overlay delta for image onto an
// already-unpacked root (tunaOS#673).
//
// tuna-os/<variant>:<flavor> images have a customize delta published at
// tuna-os/live-overlay:<variant>-<flavor> carrying the live payload CI media
// ship — installer flatpak, autostart, polkit rules. Grafting it is what makes
// a natively-built ISO byte-comparable with the browser-built one; without it
// the two paths diverge silently, which is how 27 broken overlays went
// unnoticed.
//
// Layers already present in baseManifest are skipped, so only the delta is
// fetched. Absence of an overlay is NOT an error — most variants don't have
// one yet, and treating it as a hard dependency would deadlock every new
// variant (the overlay is produced by the very build that would then require
// it). Callers get applied=false and should fall through to the plain
// baseline; the returned error is reserved for an overlay that exists but
// could not be applied.
//
// progress, if non-nil, is called as layers land.
func GraftLiveOverlay(
	root *oci.Node,
	store oci.BlobStore,
	c *oci.Client,
	image string,
	baseManifest *oci.Manifest,
	progress func(layer, total int),
) (applied bool, err error) {
	return GraftLiveOverlayForPlatform(root, store, c, image, baseManifest, "amd64", "", progress)
}

// GraftLiveOverlayForPlatform is the platform-aware overlay graft: it
// resolves the overlay with ResolveManifestForPlatform for the exact
// requested platform (never falling back across architectures or variants)
// and verifies the optional expected overlay platform-manifest digest
// (BuildRequest ResolvedInputs.LiveOverlayDigest) against the overlay
// index's MATCHING descriptor — the platform digest and any index digest
// are never conflated.
//
// A non-empty expectedDigest makes the overlay required: a missing overlay
// or a digest mismatch returns a typed *OverlayMismatchError instead of
// assembling an uncustomized live root. With no pin the historical
// (false, nil) no-op is preserved for variants without a published
// overlay, and the --rootfs-tar nil-client/member path stays a no-op.
//
// progress, if non-nil, is called as layers land.
func GraftLiveOverlayForPlatform(
	root *oci.Node,
	store oci.BlobStore,
	c *oci.Client,
	image string,
	baseManifest *oci.Manifest,
	platform, expectedDigest string,
	progress func(layer, total int),
) (bool, error) {
	applied, _, err := GraftLiveOverlayWithOptions(root, store, c, image, baseManifest, GraftOptions{
		Platform:              platform,
		ExpectedOverlayDigest: expectedDigest,
		Progress:              progress,
	})
	return applied, err
}

// GraftLiveOverlayWithOptions is GraftLiveOverlayForPlatform with a
// GraftOptions parameter. It additionally returns the resolved overlay
// platform manifest (nil when nothing was grafted) so callers can run
// VerifyLiveOverlayBase against the selected base digest.
func GraftLiveOverlayWithOptions(
	root *oci.Node,
	store oci.BlobStore,
	c *oci.Client,
	image string,
	baseManifest *oci.Manifest,
	opts GraftOptions,
) (applied bool, overlayManifest *oci.Manifest, err error) {
	if c == nil || root == nil || baseManifest == nil {
		return false, nil, nil
	}
	ref, ok := LiveOverlayRef(image)
	if !ok {
		return false, nil, nil
	}
	platform := strings.TrimSpace(opts.Platform)
	if platform == "" {
		platform = "amd64"
	}
	expected := strings.TrimSpace(opts.ExpectedOverlayDigest)
	required := opts.Required || expected != ""
	m, _, err := c.ResolveManifestForPlatformWithDigest(ref, platform, expected)
	if err != nil {
		var notFound *oci.PlatformNotFoundError
		var digestMismatch *oci.ManifestDigestMismatchError
		switch {
		case errors.As(err, &digestMismatch):
			// The overlay exists but is not the pinned one — always a
			// blocker, even for otherwise-optional overlays: grafting
			// the wrong delta is worse than grafting none.
			return false, nil, &OverlayMismatchError{Image: image, Reason: OverlayReasonDigestMismatch, Want: digestMismatch.Want, Got: digestMismatch.Got}
		case errors.As(err, &notFound):
			if !required {
				return false, nil, nil
			}
			return false, nil, &OverlayMismatchError{Image: image, Reason: OverlayReasonMissing, Want: platform, Got: strings.Join(notFound.Available, ", ")}
		default:
			// No overlay published for this variant — the common case.
			if !required {
				return false, nil, nil
			}
			return false, nil, &OverlayMismatchError{Image: image, Reason: OverlayReasonMissing, Want: platform, Got: err.Error()}
		}
	}
	skip := make(map[string]bool, len(baseManifest.Layers))
	for _, l := range baseManifest.Layers {
		skip[l.Digest] = true
	}
	if err := c.UnpackOnto(root, ref, m, store, skip, opts.Progress); err != nil {
		return false, nil, fmt.Errorf("apply live overlay %s:%s: %w", ref.Repo, ref.Tag, err)
	}
	return true, m, nil
}

// VerifyLiveOverlayBase checks the grafted overlay's base-digest annotation
// (org.tunaos.live-overlay.actual-base-digest, or the legacy
// org.tunaos.live-overlay.base-digest spelling) against actualBaseDigest —
// the selected base platform manifest digest — and returns a typed
// *OverlayMismatchError naming both digests on mismatch. Overlays carrying
// no annotation (or a nil/empty compare set) verify trivially: there is
// nothing pinned to check against.
func VerifyLiveOverlayBase(baseManifest, overlayManifest *oci.Manifest, actualBaseDigest string) error {
	if overlayManifest == nil {
		return nil
	}
	want := strings.TrimSpace(actualBaseDigest)
	if want == "" {
		return nil
	}
	var got string
	if overlayManifest.Annotations != nil {
		got = strings.TrimSpace(overlayManifest.Annotations[LiveOverlayActualBaseDigestAnnotation])
		if got == "" {
			got = strings.TrimSpace(overlayManifest.Annotations[LiveOverlayBaseDigestAnnotation])
		}
	}
	if got == "" {
		return nil
	}
	if got != want {
		return &OverlayMismatchError{Reason: OverlayReasonBaseDigestMismatch, Want: want, Got: got}
	}
	return nil
}
