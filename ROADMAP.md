# Tacklebox Roadmap

**Last updated**: 2026-10-13

Part of the [TunaOS](https://tunaos.org) ecosystem. Multi-boot media orchestrator for bootc.

## Done

- ✅ `build` — ISO and block device provisioning
- ✅ `update` — in-place env refresh without reformatting
- ✅ `add` / `remove` — mutate existing media: add or drop an environment
- ✅ `verify` — sanity-check built media
- ✅ `status` — inspect installed environments
- ✅ `update-all` — cross-env boot-time updater
- ✅ Multi-env dedup ISOs (`shared_store.dedup`)
- ✅ `recipe-gen` — YAML → recipe JSON
- ✅ USB pre-flight — unmount busy partitions before format
- ✅ Cross-platform GUI multi-boot USB manager (`tuna-os/iso-builder` native app): Inspection, add/remove/update lifecycle, cross-platform helper VMs (macOS QEMU, Windows WSL2), and VM boot verification (#104)
- ✅ CI pipeline: lint, unit, block smoke, ISO smoke, 6-env scale test

## Near-term: First Versioned Release Gate (Tracking #237)

Tacklebox is consumed by downstream TunaOS build tooling but has not yet published a versioned release. Before expanding the feature surface, establish the first supported baseline.

**Status (2026-10-13)**: Release gate #237 open since Aug 17 with no recent updates. Gate criteria and timeline need refresh.

The first versioned release is ready when:

- ✅ Release commit passes unit, block-device, ISO boot, and cross-platform disk smoke checks (CI green)
- ⏳ GitHub Releases contains Linux amd64 and arm64 binaries with checksums (release automation status unclear)
- ⏳ GHCR contains matching immutable version and digest references (asset publication pipeline status unclear)
- 🔴 Release notes identify supported targets, known limitations, compatibility expectations (not yet drafted)
- 🔴 TunaOS consumers replaceutable `main`/`latest` with versioned release (requires release to exist first)
- 🔴 30-day review records downstream upgrade results and feedback (gate not yet started)

**Blockers**: No release automation documented. Asset publication pipeline status unknown. Release notes template missing.

## Planned

- **GUI customization & signed bundles** — port browser ISO Builder customization panels to desktop GUI + signed native app bundles (#104)
- **Per-stateroot greenboot** — health-check + auto-rollback per env
- **Persist lifecycle** — quota, GC, migration
- **ARM64 multi-ISO** — aarch64 sd-boot + OVMF testing

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) for build, test, and merge requirements.

---

## Strategic Notes (2026-10-13 Refresh)

**Release Gate Stalled**: Issue #237 has been open since August 17 (58 days) with no visible progress on release automation, asset pipeline, or release notes. This blocks:
- Consumer adoption (no versioned release to pin to)
- Downstream stability (still using `main` branch)
- Rollback guarantees (no release artifacts to fall back to)

**Recommended Next Steps**:
1. Establish release automation decision: manual cut vs automated (like fisherman's `promote.yml`)
2. Define release notes template and supported-targets matrix
3. Create release readiness checklist and assign ownership
4. Set target date for v0.1.0 or v1.0.0 release

**Owner**: Needs human decision on release strategy before implementation can proceed.

See [CONTRIBUTING.md](CONTRIBUTING.md).
