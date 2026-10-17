# Haepari release candidate package (darwin/arm64)

This archive is an **unpublished release candidate**, not an official distribution.
It bundles the standalone Haepari CLI/controller, the paired `use-haepari` skill,
exercised examples, and the MIT `LICENSE`. Nothing here installs itself, enrolls a
UTM VM, or requires an MCP transport.

Supported scope (v0.1.0): one macOS arm64 machine, one OS user, private state
roots, already-running registered persistent VMs (never reset by Haepari).

## 1. Verify the package

The sidecar `<archive>.tar.gz.sha256` records the archive's bare file name:
keep the archive and sidecar together and run
`shasum -a 256 -c` from the directory holding both — checksum entries
resolve against the current working directory, not the sidecar's own path.
It binds the whole archive (including `SHA256SUMS`). `SHA256SUMS` inside
the archive lists every file **except itself**.

```sh
shasum -a 256 -c haepari-<version>-darwin-arm64.tar.gz.sha256   # archive digest
cd haepari-<version>-darwin-arm64 && python3 verify_tree.py
```

`verify_tree.py` (packaged, standard library only, independent of the haepari
binary) compares the complete unpacked tree against the inventory and reports
each **missing**, **added** or **tampered** file by name. It never follows
symlinks: an unlisted symlink or special file is reported as added, a recorded
regular file replaced by a symlink, special file or directory is reported as
tampered even when the target bytes are identical, and `SHA256SUMS` itself must
be a regular file. Ordinary structural directories that the inventory does not
list are fine. Plain `shasum -a 256 -c SHA256SUMS` only covers recorded files
and cannot detect extra files; use the packaged helper for complete-tree
verification. A failed check leaves all files in place for inspection.

## 2. Run the binary standalone

The binary needs no source checkout or plugin cache:

```sh
env -i PATH=/usr/bin:/bin HOME="$PWD/home" ./haepari version --json
env -i PATH=/usr/bin:/bin HOME="$PWD/home" ./haepari capabilities --json
env -i PATH=/usr/bin:/bin HOME="$PWD/home" ./haepari help --json
```

`version` reports the stamped release version, platform, VCS identity, and the
executable SHA-256. `capabilities` reports the frozen protocol capability set
(`managed-execution/v1`, `durable-observation/v1`, `advisory-lock/v1`,
`managed-recovery/v1`, `artifact-delivery/v1`, `evidence-retention/v1`), the
compiled bootstrap backend, and `qualified_backends` separately. Compiled
support is not live readiness: `qualified_backends` stays empty until real
backend qualification is recorded as a separate, digest-bound fact. Consumers
check these declared schemas/backends/capabilities instead of guessing from
the version string.

## 3. Start a controller

Foreground (private root; keep socket path short):

```sh
mkdir -p "$PWD/home/hp" && chmod 700 "$PWD/home/hp"
export HAEPARI_HOME="$PWD/home/hp"
./haepari daemon run --backend local
```

Set the same absolute `HAEPARI_HOME` in the client terminal before running
the examples below. For a service installation, use its planned state root.

Or install the explicit user release service (user LaunchAgent, Aqua session,
no root): plan first, review the pinned digest/root/label, then install as a
separate action. `service install` refuses an existing plist; `uninstall`
refuses while any resource is occupied and removes only its exact plist,
preserving state and evidence.

```sh
./haepari service plan --release --instance <unique-id> \
  --binary "$PWD/haepari" --root <absolute-private-root> --json > plan.envelope.json
# Save the plan's data object; install/uninstall consume it, not the envelope.
python3 -c 'import json,sys; json.dump(json.load(open("plan.envelope.json"))["data"], open("plan.json","w"), indent=2)'
# review plan.json (label io.rootkernel.haepari.<unique-id>, digest, plist)
./haepari service install --plan-file plan.json --json
# Select this service for subsequent resource/job/lock client commands.
export HAEPARI_HOME=<same-absolute-private-root>
# Run the examples, wait for all work to settle, then remove the service:
./haepari service uninstall --plan-file plan.json --json
```

## 4. Localize and run the examples

The resource examples are ready as-is. The job examples carry two clearly
marked placeholders that the validators reject until localized:
`<BUNDLE_ROOT>` and `<TREE_SHA256>`.

1. Create the selected files, e.g. `bundle/source/input.txt` (and
   `bundle/source/scenarios.md` for the manual example).
2. Compute the public `haepari-tree-sha256/v1` digest over the sorted
   selected files (this is the documented algorithm, shown as a Python
   snippet — run it from `bundle/`):

   ```python
   import hashlib, struct, pathlib
   digest = hashlib.sha256(b"haepari-tree-sha256/v1\0")
   for name in ["source/input.txt"]:  # sorted UTF-8; list every selected file
       data = pathlib.Path(name).read_bytes()
       encoded = name.encode()
       executable = bool(pathlib.Path(name).stat().st_mode & 0o111)
       digest.update(struct.pack(">Q", len(encoded)) + encoded + bytes([executable]))
       digest.update(struct.pack(">Q", len(data)) + hashlib.sha256(data).digest())
   print(digest.hexdigest())
   ```

3. Replace `<BUNDLE_ROOT>` with the absolute bundle path and `<TREE_SHA256>`
   with the printed digest. Then register the resource and run:

   ```sh
   ./haepari resource register --file examples/resource-local.json --json
   ./haepari submit --spec examples/job-local-auto.json --request-key install-check-1 --json
   ./haepari wait <job-id> --timeout 60s --json   # exit 0 PASS / 1 FAIL / 3 INCOMPLETE / 4 CANCELLED
   ```

   For an uncertain transport reply, resend the **same** key and spec; never
   a new key. Read `error.code` and `error.retry` first: exit 6 also covers
   storage failures. The paired skill (`skill/use-haepari/SKILL.md`) documents the full
   submit-once/wait/reconnect/cancel/recovery behavior.

## 5. Advisory lock example

```sh
./haepari resource register --file examples/resource-lock.json --json
./haepari lock acquire pkg-review --request-key review-1 --holder my-tool --wait --json
./haepari lock keepalive <grant-id> --json &   # while the protected work runs
./haepari lock release <grant-id> --json       # on every completion path
```

A nested invocation receiving `HAEPARI_LOCK_GRANT` validates the inherited
grant read-only and uses it; it never renews, releases, or cancels the
parent's grant. Locks grant no execution permit and count toward no managed
capacity.

## 6. Boundaries

- Unpublished candidate: a local build/test is not distribution, host
  installation, or publication authority.
- No auto-install, no UTM auto-enrollment, no required MCP transport, no
  external lease commands in v0.1.0.
- Never delete `~/.aquarium-dev`, foreign Git hooks, Sorage data, or host
  skills; Haepari owns only its own state root and job workspaces.
- `qualified_backends` emptiness is truthful until real backend
  qualification is recorded separately.
