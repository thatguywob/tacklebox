// Offline installation sources embedded in pure live media.
//
// A curated live ISO carries, next to its bootable EROFS rootfs, the
// verified read-only OCI layout the installed system comes from: the base
// platform manifest, its config blob, and its compressed base layers — plus
// a MediaInput v1 descriptor telling Fisherman which manifest to install
// and which ref the installed system tracks for day-2 updates.
//
// Shape of the on-media layout (Fisherman PreflightOCILayout):
//
//	<layout>/oci-layout          {"imageLayoutVersion":"1.0.0"} (static)
//	<layout>/index.json          synthesized single-manifest index pointing
//	                             at the verified platform manifest digest
//	<layout>/blobs/sha256/<hex>  platform manifest, config, and every base
//	                             layer, addressed exactly as descriptors name
//	                             them
//
// The layout holds the BASE platform image only. Live customization layers
// (grafted overlay deltas, EnsureLiveUser/EnsureAutologin edits, remora
// manifests, flatpak preloads) live in the bootable rootfs, never here.
//
// Trust chain: layer bytes are retained from the single verified unpack
// pass (oci.Client.RetainLayer, digest-checked by the client's verifying
// reader on Close) and re-opened from retention at ISO-write time — never
// re-downloaded, never heap-buffered. The platform manifest bytes cannot be
// reconstructed from their decoded form (re-marshalling would not reproduce
// the digest-identical document), so they are re-fetched once, by digest,
// over the client's authenticated transport and hash-verified
// (FetchManifestBytes). The config blob is likewise fetched once by digest;
// it is never downloaded during unpack. Media-input and layout digests are
// cross-checked against the unpacked manifest before anything is authored,
// so a mismatch fails the build instead of shipping unidentified media.
package purefs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/tuna-os/tacklebox/internal/oci"
)

const (
	// OfflineLayoutDir is the ISO-tree directory carrying the embedded OCI
	// layout. The live initrd mounts the ISO at LiveISOMountPoint, so the
	// layout's live-system path is OfflineLayoutLivePath.
	OfflineLayoutDir = "/OCI"
	// LiveISOMountPoint is where tbox-live mounts the ISO at boot — the
	// same mount the EROFS rootfs image is read from.
	LiveISOMountPoint = "/run/initramfs/live"
	// OfflineLayoutLivePath is the ociLayoutPath written into
	// media-input.json: the on-media layout as seen from the live system.
	OfflineLayoutLivePath = LiveISOMountPoint + OfflineLayoutDir
	// OfflineMediaKind is the MediaInput v1 kind builders write. Opaque to
	// Fisherman.
	OfflineMediaKind = "tacklebox-live"
	// OfflineMediaSchemaVersion is the MediaInput contract version builders write.
	OfflineMediaSchemaVersion = 1
	// MediaInputTreePath is the live-root-relative path of media-input.json.
	// Fisherman reads it at the host path /etc/bootc-installer/media-input.json.
	MediaInputTreePath = "etc/bootc-installer/media-input.json"
	// LiveISOModeTreePath is the live-root-relative path of the live-mode
	// signal file. An empty file: Fisherman's live probe treats existence
	// as the opt-in, never content.
	LiveISOModeTreePath = "etc/bootc-installer/live-iso-mode"
	// defaultOCIManifestMediaType is recorded in the synthesized index when
	// the resolved manifest carries no media type of its own.
	defaultOCIManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	// defaultOCIIndexMediaType is the media type of the synthesized index.
	defaultOCIIndexMediaType = "application/vnd.oci.image.index.v1+json"
	// maxOfflineManifestBytes caps the one blob OCILayoutInputs buffers to
	// learn its exact ISO size (see OCILayoutInputs). Image manifests are
	// kilobytes; anything larger is not a manifest.
	maxOfflineManifestBytes = 8 << 20
)

// validOfflineDigest reports whether d is a strict "sha256:" + 64 lowercase
// hex digest — the machine-readable shape Fisherman validates. Uppercase hex
// is rejected even though some validators tolerate it: digests become blob
// filenames and JSON identity, where two spellings of one digest would be
// two different files.
func validOfflineDigest(d string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(d, prefix) || len(d) != len(prefix)+64 {
		return false
	}
	for i := len(prefix); i < len(d); i++ {
		c := d[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// SplitDigest splits a strict offline digest into algorithm and hex for blob
// paths (blobs/<alg>/<hex>). It rejects anything validOfflineDigest rejects,
// so retained-blob filenames can never escape their directory.
func SplitDigest(digest string) (alg, hexPart string, err error) {
	if !validOfflineDigest(digest) {
		return "", "", fmt.Errorf("invalid digest %q: must be sha256:<64 lowercase hex>", digest)
	}
	return "sha256", digest[len("sha256:"):], nil
}

// offlineBlobPath maps a digest to its ISO-tree path inside the layout.
func offlineBlobPath(digest string) (string, error) {
	alg, hexPart, err := SplitDigest(digest)
	if err != nil {
		return "", err
	}
	return OfflineLayoutDir + "/blobs/" + alg + "/" + hexPart, nil
}

// offlineMediaInput is the MediaInput v1 document in field order. Every key
// is always emitted (no omitempty): the contract is an exact 11-key object,
// and Fisherman treats absent and empty digests alike.
type offlineMediaInput struct {
	SchemaVersion          int    `json:"schemaVersion"`
	Kind                   string `json:"kind"`
	Image                  string `json:"image"`
	PlatformManifestDigest string `json:"platformManifestDigest"`
	IndexDigest            string `json:"indexDigest"`
	ConfigDigest           string `json:"configDigest"`
	OCILayoutPath          string `json:"ociLayoutPath"`
	TargetImgref           string `json:"targetImgref"`
	Platform               string `json:"platform"`
	CatalogVersion         string `json:"catalogVersion"`
	QualificationID        string `json:"qualificationID"`
}

// normalizeMediaPlatform folds a builder platform ("linux/amd64",
// "linux/amd64/v2", or bare "amd64") into the "os/arch" form Fisherman
// matches against the host. The CPU variant never survives: Fisherman cuts
// the platform at the first slash, so a variant would poison the arch
// comparison.
func normalizeMediaPlatform(platform string) (string, error) {
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return "", fmt.Errorf("media platform is empty")
	}
	if !strings.Contains(platform, "/") {
		platform = "linux/" + platform
	}
	osPart, archPart, _ := strings.Cut(platform, "/")
	if osPart != "linux" || archPart == "" || strings.Contains(archPart, "/") {
		return "", fmt.Errorf("media platform %q must be linux/<arch>", platform)
	}
	arch, err := EffectivePlatformArch(archPart)
	if err != nil {
		return "", fmt.Errorf("media platform %q: %w", platform, err)
	}
	return "linux/" + arch, nil
}

// WriteMediaInputJSON renders a Fisherman-compatible MediaInput v1 document.
// image/targetImgref are the frozen request's image ref (targetImgref is the
// day-2 update ref); platformManifestDigest is the verified platform
// manifest digest; indexDigest/configDigest are the pinned digests
// (informational, may be empty); ociLayoutPath is the live-system layout
// path (OfflineLayoutLivePath); platform is "os/arch" (a bare arch is
// accepted); catalogVersion/qualificationID are opaque provenance.
func WriteMediaInputJSON(image, platformManifestDigest, indexDigest, configDigest, ociLayoutPath, targetImgref, platform, catalogVersion, qualificationID string) ([]byte, error) {
	if strings.TrimSpace(image) == "" {
		return nil, fmt.Errorf("media input image is empty")
	}
	if !validOfflineDigest(platformManifestDigest) {
		return nil, fmt.Errorf("media input platformManifestDigest %q must be sha256:<64 lowercase hex>", platformManifestDigest)
	}
	if indexDigest != "" && !validOfflineDigest(indexDigest) {
		return nil, fmt.Errorf("media input indexDigest %q must be sha256:<64 lowercase hex>", indexDigest)
	}
	if configDigest != "" && !validOfflineDigest(configDigest) {
		return nil, fmt.Errorf("media input configDigest %q must be sha256:<64 lowercase hex>", configDigest)
	}
	if ociLayoutPath == "" || !path.IsAbs(ociLayoutPath) {
		return nil, fmt.Errorf("media input ociLayoutPath %q must be absolute", ociLayoutPath)
	}
	if strings.TrimSpace(targetImgref) == "" {
		return nil, fmt.Errorf("media input targetImgref is empty (image %q needs a day-2 update ref)", image)
	}
	platform, err := normalizeMediaPlatform(platform)
	if err != nil {
		return nil, err
	}
	return json.Marshal(offlineMediaInput{
		SchemaVersion:          OfflineMediaSchemaVersion,
		Kind:                   OfflineMediaKind,
		Image:                  image,
		PlatformManifestDigest: platformManifestDigest,
		IndexDigest:            indexDigest,
		ConfigDigest:           configDigest,
		OCILayoutPath:          ociLayoutPath,
		TargetImgref:           targetImgref,
		Platform:               platform,
		CatalogVersion:         catalogVersion,
		QualificationID:        qualificationID,
	})
}

// ShouldEmbedOfflineLayout reports whether the build carries an embedded
// offline layout. The layout ships exactly when a BuildRequest pins exact
// source identity via manifest/config digests: custom images without pins
// keep their existing behavior, and builds with no pinned digests preserve
// the old media shape (where the offline-store/self-install route already
// proves source identity).
func ShouldEmbedOfflineLayout(req *BuildRequest) bool {
	if req == nil || req.ResolvedInputs == nil {
		return false
	}
	ri := req.ResolvedInputs
	return ri.PlatformManifestDigest != "" || ri.IndexDigest != "" || ri.ConfigDigest != ""
}

// WriteLiveMarkerFiles writes the two live-root signals before rootfs
// authoring: the live-iso-mode opt-in (empty file) and media-input.json.
// Both land inside the bootable rootfs (Fisherman reads them at host
// /etc paths), never in the ISO tree.
func WriteLiveMarkerFiles(root *oci.Node, store oci.BlobStore, mediaInputJSON []byte) error {
	if root == nil {
		return fmt.Errorf("live marker files: nil rootfs tree")
	}
	if store == nil {
		return fmt.Errorf("live marker files: nil blob store")
	}
	if len(mediaInputJSON) == 0 {
		return fmt.Errorf("live marker files: empty media-input.json")
	}
	if err := writeFileNode(root, store, LiveISOModeTreePath, "", 0o644); err != nil {
		return fmt.Errorf("live marker files: %w", err)
	}
	if err := writeFileNode(root, store, MediaInputTreePath, string(mediaInputJSON), 0o644); err != nil {
		return fmt.Errorf("live marker files: %w", err)
	}
	return nil
}

// offlineDescriptor is one entry of the synthesized index.json. Size and
// platform are omitted when unknown: Fisherman checks a blob's size only
// when its descriptor carries one, and selects the manifest by digest, so
// neither is load-bearing — the digest is.
type offlineDescriptor struct {
	MediaType string           `json:"mediaType"`
	Digest    string           `json:"digest"`
	Size      int64            `json:"size,omitempty"`
	Platform  *offlinePlatform `json:"platform,omitempty"`
}

// offlinePlatform is the descriptor platform as Fisherman decodes it.
type offlinePlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

// offlineIndex is the synthesized single-manifest index.json.
type offlineIndex struct {
	SchemaVersion int                 `json:"schemaVersion"`
	MediaType     string              `json:"mediaType"`
	Manifests     []offlineDescriptor `json:"manifests"`
}

// digestOpener re-opens a retained blob for each ISO read: the layout
// streams, so every Source must be callable exactly once at write time and
// must hold no content in memory beforehand.
func digestOpener(store oci.BlobStore, digest string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		return store.Open(digest)
	}
}

// readCappedBlob buffers one small blob to learn its exact ISO size. Only
// the manifest takes this path: its size is unknowable otherwise (the
// decoded manifest cannot reproduce its digest-identical bytes, and the
// index entry deliberately carries no size). Layers and config stream
// purely by descriptor size and are never buffered here.
func readCappedBlob(store oci.BlobStore, digest string) ([]byte, error) {
	rc, err := store.Open(digest)
	if err != nil {
		return nil, fmt.Errorf("open blob %s: %w", digest, err)
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, maxOfflineManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", digest, err)
	}
	if len(body) > maxOfflineManifestBytes {
		return nil, fmt.Errorf("blob %s exceeds %d bytes: not an image manifest", digest, maxOfflineManifestBytes)
	}
	return body, nil
}

// OCILayoutInputs builds the streaming ISO inputs for the embedded OCI
// layout: oci-layout, the synthesized single-manifest index.json, and one
// blobs/sha256/<hex> entry each for the platform manifest, the config, and
// every base layer in manifest.
//
// store must open retained blobs by full digest ("sha256:<hex>") —
// production call sites front their retention (arena or files) with exactly
// such a mapping. indexDigest is the verified platform manifest digest to
// index (it keys the manifest blob and becomes manifests[0].digest); the
// registry's top-level index digest, when different, is provenance recorded
// in media-input.json, not embedded — the on-media index is always the
// synthesized single-manifest form. configDigest, when set, must equal the
// manifest's config digest or the build fails instead of shipping a layout
// that disagrees with its own manifest.
//
// Callers must have verified every retained blob against its descriptor
// size before calling: IsoInput sizes come from the descriptors, and the
// layout writer reads each source exactly once at ISO-write time.
func OCILayoutInputs(store oci.BlobStore, manifest *oci.Manifest, indexDigest, configDigest string) ([]IsoInput, error) {
	if store == nil {
		return nil, fmt.Errorf("offline layout: nil blob store")
	}
	if manifest == nil {
		return nil, fmt.Errorf("offline layout: nil manifest")
	}
	if !validOfflineDigest(indexDigest) {
		return nil, fmt.Errorf("offline layout: indexDigest %q must be sha256:<64 lowercase hex>", indexDigest)
	}
	if configDigest != "" && configDigest != manifest.Config.Digest {
		return nil, fmt.Errorf("offline layout: configDigest %s does not match manifest config %s", configDigest, manifest.Config.Digest)
	}
	if !validOfflineDigest(manifest.Config.Digest) {
		return nil, fmt.Errorf("offline layout: manifest config digest %q must be sha256:<64 lowercase hex>", manifest.Config.Digest)
	}
	if len(manifest.Layers) == 0 {
		return nil, fmt.Errorf("offline layout: manifest has no layers")
	}
	for i, l := range manifest.Layers {
		if !validOfflineDigest(l.Digest) {
			return nil, fmt.Errorf("offline layout: layer %d digest %q must be sha256:<64 lowercase hex>", i, l.Digest)
		}
		if l.Size < 0 {
			return nil, fmt.Errorf("offline layout: layer %d (%s) has negative size %d", i, l.Digest, l.Size)
		}
	}

	manifestMediaType := manifest.MediaType
	if manifestMediaType == "" {
		manifestMediaType = defaultOCIManifestMediaType
	}
	indexBytes, err := json.Marshal(offlineIndex{
		SchemaVersion: 2,
		MediaType:     defaultOCIIndexMediaType,
		Manifests: []offlineDescriptor{
			{MediaType: manifestMediaType, Digest: indexDigest},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("offline layout: encode index.json: %w", err)
	}

	manifestBytes, err := readCappedBlob(store, indexDigest)
	if err != nil {
		return nil, fmt.Errorf("offline layout: %w", err)
	}
	manifestPath, err := offlineBlobPath(indexDigest)
	if err != nil {
		return nil, fmt.Errorf("offline layout: %w", err)
	}
	configPath, err := offlineBlobPath(manifest.Config.Digest)
	if err != nil {
		return nil, fmt.Errorf("offline layout: %w", err)
	}

	ociLayoutBytes := []byte(`{"imageLayoutVersion":"1.0.0"}`)
	inputs := []IsoInput{
		{Path: OfflineLayoutDir + "/oci-layout", Size: int64(len(ociLayoutBytes)), Source: StringSource(string(ociLayoutBytes))},
		{Path: OfflineLayoutDir + "/index.json", Size: int64(len(indexBytes)), Source: StringSource(string(indexBytes))},
		{Path: manifestPath, Size: int64(len(manifestBytes)), Source: digestOpener(store, indexDigest)},
		{Path: configPath, Size: manifest.Config.Size, Source: digestOpener(store, manifest.Config.Digest)},
	}
	for _, l := range manifest.Layers {
		p, err := offlineBlobPath(l.Digest)
		if err != nil {
			return nil, fmt.Errorf("offline layout: %w", err)
		}
		inputs = append(inputs, IsoInput{Path: p, Size: l.Size, Source: digestOpener(store, l.Digest)})
	}
	return inputs, nil
}

// FetchManifestBytes re-fetches one manifest document by digest over the
// client's authenticated transport and verifies the bytes hash to the
// digest before returning them. Manifest payloads are kilobytes; this is
// not a layer re-download. It exists because resolution decodes the
// manifest and discards the document, and only the digest-identical bytes
// may be embedded as the layout's manifest blob.
func FetchManifestBytes(c *oci.Client, ref oci.Ref, digest string) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("manifest fetch: nil oci client")
	}
	if !validOfflineDigest(digest) {
		return nil, fmt.Errorf("manifest fetch: digest %q must be sha256:<64 lowercase hex>", digest)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	timeout := c.HeaderTimeout
	if timeout <= 0 {
		timeout = oci.DefaultHeaderTimeout
	}
	base := strings.TrimRight(c.Base, "/")
	token, err := fetchRegistryToken(httpClient, base, ref.Repo, timeout)
	if err != nil {
		return nil, fmt.Errorf("manifest fetch %s: %w", digest, err)
	}
	body, err := fetchRegistryManifestBytes(httpClient, base, ref.Repo, digest, token, timeout)
	if err != nil {
		return nil, fmt.Errorf("manifest fetch %s: %w", digest, err)
	}
	sum := sha256.Sum256(body)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != digest {
		return nil, fmt.Errorf("manifest fetch: digest mismatch: got %s, want %s", got, digest)
	}
	return body, nil
}
