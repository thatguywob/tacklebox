# X13s live-loader prototype

This is an isolated native validation/staging library. It is not wired into the
recipe CLI yet and cannot produce an X13s test ISO by itself. Generic native and
browser builds are unchanged. It does not support installation, persistent/block
targets, or offline payloads.

## Input contract

`internal/liveboot.Manifest` describes the explicit mode
`fedora-arm64-dtbloader`, architecture `arm64`, board
`lenovo,thinkpad-x13s`, expected kernel release and SHA-256, and four descriptors:
`shim`, `grub`, `loader`, and `preparation_receipt`. Each descriptor contains a
relative `path`, exact positive `size`, and lowercase `sha256`. EFI files are
bounded at 64 MiB; the receipt is bounded at 1 MiB. No input is downloaded.

Payload preparation is Linux-only; other hosts return an explicit unsupported
error rather than using weaker path checks. Portable validation still compiles
on other platforms. All path components are opened without following symlinks, including the
preparation directory. Inputs must be regular files with the pinned size;
post-read identity and timestamp checks supplement the digest of the actual
returned bytes. The preparation receipt is caller-supplied provenance, retained
verbatim. Binding its digest does not validate its signature claims.

`ValidateScope` rejects anything except one live-only ISO environment with a
SHA-256-pinned image, no dedup, no offline payloads and no initramfs skip.
`Prepare` also enforces that scope and exactly one mandatory gate argument,
rejects blacklist arguments, and validates bounded ARM64 PE files, matches the loader's meaningful
`.linux` bytes exactly to the extracted stock kernel, matches the release, and
checks that the static hardware-ID table and a DTB both name the X13s. This last
check is table linkage, not proof of physical SMBIOS selection.

The returned bundle preserves original EFI bytes, supplies Fedora shim/GRUB
paths and GRUB configuration for an external initrd, and replaces only the live
media's staged kernel with the matching loader. It never changes the source
image's kernel RPM transaction. GRUB argument syntax is intentionally narrow;
unsupported quoting/interpolation is rejected rather than guessed.

## Required consumer integration

Before exposing this through the CLI, the orchestrator must:

1. Validate scope before preparing any target and verify image architecture.
2. Force the manifest's exact kernel release during initramfs creation and boot
   extraction. Verify module/package ABI metadata against the same kernel build.
3. Require the X13s software-order barrier and early ADSP module/firmware closure
   in the actual emitted initrd. Reject stock/cache shortcuts lacking the exact
   reviewed hook/helper bytes. Validate the initrd actually referenced on media.
4. Inject the mandatory X13s gate argument, use byte-preserving bundle staging,
   and mirror every required EFI path into both FAT and ISO roots. No implicit
   host loader fallback is allowed.
5. Record source image digest, payload/initrd hashes, module and firmware
   inventories, preparation receipt, and final artifact checksum. A caller's
   receipt and independently verified preparation results must remain distinct.
6. Disable internal-disk automount/writes for the experimental live session and
   document explicit stop conditions and residual USB reset risk.

The separate `70tbox-x13s-adsp` module is presently an unembedded prototype. Its
mandatory firmware checks also validate selected compression against the kernel
config. Tests replay Fedora108's actual install-before-installkernel control
flow: install failures propagate through the checked kernel hook, and no-kernel
mode aborts explicitly. The extracted control-flow fixture retains its upstream
GPL-2.0-or-later notice and license.

The software barrier must defer early cmdline callers, then bound waiting after
udev for the selected ADSP firmware to reach `running`. This is stronger than
asynchronous `modprobe` completion but is not an electrical USB-reset completion
API. Actual device behavior remains an owner's later experimental observation.
No Secure Boot compatibility or physical hardware qualification is asserted.

## Evidence and tests

The independent Fedora shim → GRUB → selected systemd-stub handoff fixture is
[tunaOS #3494](https://github.com/tuna-os/tunaOS/pull/3494). Its successful QEMU
run includes external-initrd/command-line sentinels and missing-initrd/wrong-arg
controls; it does not emulate an X13s or spoof its SMBIOS.

Local package tests include negative path, hash, size, target, PE, kernel,
board-table and GRUB-argument cases. The optional
`TBOX_FEDORA_LOADER_FIXTURE` directory supplies `loader.efi` and `vmlinuz` from
pinned Fedora 44 7.2.11-200 RPMs; the test checks exact published byte hashes
before parsing them. Local checks use a supplemental Go 1.24 standalone harness.
The repository requires Go 1.26.3; full mandatory build/test/vet/integration-help
checks are still pending native CI. No final image/initrd/media exists yet.
