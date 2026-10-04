# TASK-014 authenticated guest runner boundary

TASK-014 adds the internal pinned SSH transport and per-Job native guest entrypoint. The [canonical roadmap](../roadmap.md) alone owns its status. [SSH and guest supervision](../contracts/resource-and-runner.md#ssh-and-guest-supervision) owns the protocol and authentication semantics.

## Delivered boundary

- A digest-bound private mapping selects one user, port and explicit key reference. OpenSSH receives an isolated configuration, a snapshot of the dedicated ed25519 pin, strict checking and disabled agent, forwarding and interactive authentication. Key bytes and ambient host environment are not transferred.
- Before each operation, current UTM power and one unexpired individual MAC lease lead to authenticated native guest MAC/platform/boot proof with a fresh challenge. Guest UUID identifies the registered host-key alias; it is not independently discovered from UTM metadata inside the guest.
- A constant bootstrap consumes three fixed archive entries and verifies the transferred executable. Structured request data carries the sealed input inventory, literal argv/environment, original input/profile digests, phase deadline and exact run identity; replies bind its canonical digest. Fresh private Job directories receive the runner and regular files with hash/mode validation and durable publication before execution.
- Native runner receipts retain the existing file-sync, exclusive rename and directory-sync protocol. A separate durable launch claim precedes detached supervision. Original receipts/output can be observed again; conflicting identity, reboot and ambiguous launch refuse a new execution. A complete preparation can be observed again; partial preparation is never silently completed on replay.
- Cancellation requests the original run and requires proven workload termination. A cancelled or disconnected transport proves neither termination nor safe reuse. Issued-operation failure retains uncertainty and never retries automatically.

## Verification and limits

Unit checks cover fixed argv/request encoding, pin/authentication reference refusal, wrong VM/MAC/challenge/boot/platform, expired leases and one issued operation after response loss. Integration fixtures execute the actual native supervisor and product binary, including the exact bootstrap over a local subprocess transport, original receipts/output, one execution, a reaped delayed transport observer while the workload survives, cancellation and an ambiguous launch that never spawns again. These fixtures operate no SSH server or UTM VM. Fake authentication rejection plus inspected OpenSSH policy is not a real host-key negotiation observation.

No optional real-guest smoke is claimed. A diagnostic SSH alias alone supplies neither the current registered UUID/MAC nor a dedicated product pin and mapping. Provisioning, connecting to an unregistered VM and changing guest login/authentication are separate authorities. Actual UTM/SSH and installed development-service qualification remain TASK-016 obligations. This checkpoint does not qualify power-loss durability or F2.

## Remaining owners

TASK-015 connects the transport and durable guest request to Job admission, resource leases, phase deadlines, collection, cleanup, quarantine and recovery. It must preserve issued-effect uncertainty, original native request/boot identity, partial preparation and ambiguous launch; no lost transport response permits a second workload or automatic file deletion. The transport's internal JSON/runner limits must be checked before owned guest preparation, with explicit failure rather than input truncation. TASK-015 now owns the integrated boundary in its [VM Job recovery record](TASK-015-vm-job-recovery.md); removal remains unsupported.

TASK-016 performs the mandatory real backend campaign and freeze inventory. Release notes are not enrolled in this repository. Neither this record nor a member commit authorizes installation, publication or release.
