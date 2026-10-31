# First-release handoff

Checkpoint: 2026-10-31. Execution tracker: [#408](https://github.com/tuna-os/tacklebox/issues/408).

## Confirmed ownership and dates

[Maintainer authorization](https://github.com/tuna-os/tacklebox/issues/408#issuecomment-6514133869)
confirms @hanthor as release owner and 30-day review owner, **v0.1.0** as the
initial version, and **2026-11-08** as the publication target. Tag publication
stays with @hanthor. No tag or GitHub Release exists at this checkpoint.

The review is scheduled for **2026-12-08** if publication happens on November 8.
Record the actual publication date below and move the review to that date plus
30 days if publication moves.

## Selected candidate and evidence

Candidate: **`5cc970f316615937c9caa5b7b9244381992cf0b1`**, an ancestor of `main`.
This is the latest main commit with the complete required passing evidence at
this checkpoint. Newer commits have cancelled or incomplete `ci` runs; the
current tip `4b307e7f9b9f12fa48ea34d825add4c22675c824` has not passed the full gate.

All evidence below reports the exact candidate SHA:

| Gate | Passing evidence |
| --- | --- |
| Unit, vet, build, recipe and CLI help checks | [lint-test](https://github.com/tuna-os/tacklebox/actions/runs/40673598075/job/122766259133) |
| Block build, verify, update, add/remove and QEMU boot | [verify-smoke](https://github.com/tuna-os/tacklebox/actions/runs/40673598075/job/122766665280) |
| ISO layouts, dedup scale, delta, offline store and QEMU boot | [iso-smoke](https://github.com/tuna-os/tacklebox/actions/runs/40673598075/job/122766665369) |
| macOS and Windows disk primitives | [cross-platform disk smoke](https://github.com/tuna-os/tacklebox/actions/runs/40029936316) |
| Development container publication | [Release container image](https://github.com/tuna-os/tacklebox/actions/runs/40029936386) |

Cross-platform smoke validates disk primitives, not the complete native GUI
helper-VM lifecycle. Development container publication does not prove a versioned
container exists.

This selection is a validated starting point, not authorization to tag the old
SHA with pending release fixes omitted. If release preparation changes the
commit, select the resulting main SHA and replace every evidence link with
passing runs for that exact SHA before publication.

## Draft release notes for the owner

These notes describe the selected candidate; reconcile them with the final SHA
and the [support and rollback notes proposed in PR #383](https://github.com/tuna-os/tacklebox/pull/383)
before publishing. That PR remains open and its notes are not yet on main.

- Tacklebox builds multi-environment bootc media with a shared systemd-boot ESP:
  disk images, physical block devices, and UEFI live ISOs. The CLI includes
  `build`, `verify`, `status`, `update`, `add`, `remove`, and `update-all`.
- The tested baseline is Linux with x86_64 QEMU/OVMF boot coverage. Linux amd64
  and arm64 binary assets are planned; an arm64 binary does not imply tested
  ARM64 multi-ISO boot support. macOS/Windows coverage is limited to the disk
  primitives listed above.
- Media creation through the CLI requires Linux root privileges and external
  tools including Podman, filesystem utilities, and bootloader tooling. Real
  block-device provisioning erases the selected target; back up its data first.
  The native pure-Go and browser builders remain separate paths and should not
  be inferred to have the same support coverage as the CLI smoke tests.
- Recipes identify environments and image inputs. Preserve the exact recipe,
  image digests and existing media backup for reproducibility. This initial
  baseline does not establish compatibility with every future recipe or media
  format; later release notes must state migrations and breaking changes.
- To upgrade, verify the downloaded binary checksum, retain the previous binary
  and container digest, and run `verify` before and after `update` on backed-up
  media. `update` preserves the persistence partition but reinstalls environment
  contents. Test on a copy before updating a physical device.
- To roll back the tool, restore the prior binary/container digest. To roll back
  environments, retain the prior recipe and image digests and reapply them with
  the compatible tool, or restore the media backup. Replacing the binary alone
  does not undo media changes. There is no earlier tagged Tacklebox release to
  fall back to for this first publication; record the prior source SHA/digest.

## Remaining publication checklist

- [x] Confirm owner, version, target and review owner from the maintainer decision.
- [x] Select the latest fully green main candidate and record exact-SHA evidence.
- [x] Prepare the draft release notes above.
- [ ] Resolve binary readiness in [PR #383](https://github.com/tuna-os/tacklebox/pull/383)
  and record the release automation decision for [#359](https://github.com/tuna-os/tacklebox/issues/359).
  Neither atomic workflow hardening nor release-please is treated as merged or
  chosen here. The current binary workflow uploads independently from each
  architecture job; publication readiness remains unconfirmed.
- [ ] Refresh candidate SHA, evidence and notes after release preparation lands.
- [ ] @hanthor publishes the tag/release and records the actual publication date,
  tag SHA, release URL and binary/container run links here and in #408.
- [ ] Verify Linux amd64 and arm64 binaries and their checksums, matching GHCR
  version tags, and immutable container digest for the published SHA.
- [ ] Record each downstream pin PR and merged commit, the new version/digest,
  and its previous source version/digest for rollback. No completed downstream
  pins are claimed at this checkpoint.
- [ ] Refresh ROADMAP at publication and downstream pin completion.
- [ ] @hanthor completes the review 30 days after actual publication.

## Publication and review record

| Record | Value |
| --- | --- |
| Actual publication date, tag SHA and release URL | Pending owner publication |
| Binary/checksum and versioned GHCR evidence | Pending publication verification |
| Downstream pin PRs, merged commits and previous pins | Pending downstream updates |
| Review owner | @hanthor |
| Review due | 2026-12-08 if published 2026-11-08; otherwise publication + 30 days |
| Review results | Pending review |

At review, record downstream upgrade and rollback outcomes, external installation
and use feedback (explicitly write “none received” if applicable), release
defects, and concrete next-version actions. Link the results from #408, the
original gate #237 (now closed), and ROADMAP.md. Keep #408 open until the
publication, downstream pin and review records are complete.
