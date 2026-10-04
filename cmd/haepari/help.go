package main

import (
	"fmt"
	"io"

	"github.com/irootkernel/haepari/internal/managed"
)

type commandHelp struct {
	Name  string `json:"name"`
	Usage string `json:"usage"`
}

// This is the supported local operator surface. VM and manual commands are
// enrolled by their later freeze gates rather than advertised prematurely.
func helpCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "--json" {
		fmt.Fprintln(stderr, "usage: haepari help --json")
		return 2
	}
	commands := []commandHelp{
		{"help", "help --json"}, {"version", "version --json"}, {"capabilities", "capabilities --json"},
		{"daemon run", "daemon run [--backend local]"},
		{"service plan", "service plan --instance <id> --binary <absolute-path> --root <private-root> --json"},
		{"service install", "service install --plan-file <json> --json"},
		{"service uninstall", "service uninstall --plan-file <json> --json"},
		{"runner start", "runner start --request <absolute-request.json> [--json]"},
		{"runner inspect", "runner inspect --request <absolute-request.json> [--json]"},
		{"runner watch", "runner watch --request <absolute-request.json> [--json]"},
		{"runner cancel", "runner cancel --request <absolute-request.json> [--json]"},
		{"resource register", "resource register --file <json> --json"},
		{"resource inspect", "resource inspect <id> --json"},
		{"resource pause", "resource pause <id> --expected-revision <n> [--expected-config-revision <n> --expected-lease-generation <n>] [--request-key <key>] [--reason <text>] --json"},
		{"resource resume", "resource resume <id> --expected-revision <n> [--expected-config-revision <n> --expected-lease-generation <n>] [--request-key <key>] --json"},
		{"resource update", "resource update --file <lock-or-vm-json> --expected-revision <n> [--expected-availability-revision <n> (required for VM)] --json"},
		{"resource remove", "resource remove <id> --expected-revision <n> --json"},
		{"resource recover", "resource recover <id> --plan [--action recollect|dispose] --json | --plan-token <token> --request-key <key> --json"},
		{"job recover", "job recover <id> --plan [--action reconcile|attest-stopped|attest-no-start] --json | --plan-token <token> --request-key <key> [--evidence-file <json>] --json"},
		{"resources list", "resources list [--cursor <cursor>] [--limit <1-100>] --json"},
		{"jobs list", "jobs list [--resource <id>] [--state <state>] [--cursor <cursor>] [--limit <1-100>] --json"},
		{"submit", "submit --spec <json> --request-key <key> --json"},
		{"run", "run --spec <json> --request-key <key> [--json]"},
		{"inspect", "inspect <job-id> --json"},
		{"wait", "wait <job-id> [--timeout <duration up to 24h>] --json"},
		{"events", "events <job-id> [--after <cursor>] [--limit <1-1000>] [--follow] --json"},
		{"logs", "logs <job-id> [--after <offset>] [--identity <id>] [--follow] [--json]"},
		{"cancel", "cancel <job-id> --expected-revision <n> [--request-key <key>] --json"},
		{"export", "export <job-id> --to <absolute-directory> [--json]"},
		{"purge", "purge <job-id> --expected-revision <n> --json"},
		{"lock acquire", "lock acquire <resource-id> --request-key <key> --holder <label> [--queue-seconds <n>] [--wait] [--timeout <duration>] --json"},
		{"lock wait", "lock wait <ticket-id> [--timeout <duration>] --json"},
		{"lock renew", "lock renew <ticket-id> --json"},
		{"lock keepalive", "lock keepalive <ticket-id> --json"},
		{"lock release", "lock release <ticket-id> [--force --reason <text>] --json"},
		{"lock cancel", "lock cancel <ticket-id> [--force --reason <text>] --json"},
		{"locks list", "locks list [--lock <resource-id>] [--cursor <cursor>] [--limit <1-100>] --json"},
	}
	errors := map[managed.ErrorCode]int{}
	for _, code := range []managed.ErrorCode{managed.InvalidRequest, managed.Conflict, managed.Unsupported, managed.PrerequisiteUnavailable, managed.NestedAcquisitionRejected, managed.NestedWaitRejected, managed.NotFound, managed.ResultUnavailable, managed.ResourceBusy, managed.LockExpired, managed.ProtocolUnsupported, managed.StaleRevision, managed.RecoveryBlocked, managed.WaitTimeout, managed.TransportUncertain, managed.StorageUnavailable} {
		exit, err := managed.ExitCode(code)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 6
		}
		errors[code] = exit
	}
	return writeJSON(stdout, stderr, "help", struct {
		Schema           string                    `json:"schema"`
		Protocol         string                    `json:"protocol"`
		Commands         []commandHelp             `json:"commands"`
		Schemas          []string                  `json:"schemas"`
		ErrorExits       map[managed.ErrorCode]int `json:"error_exits"`
		VerdictExits     map[managed.Verdict]int   `json:"verdict_exits"`
		Enums            map[string][]string       `json:"enums"`
		Cursors          map[string]string         `json:"cursors"`
		Limits           map[string]int64          `json:"limits"`
		Canonicalization string                    `json:"canonicalization"`
	}{
		Schema: "haepari.help/v1", Protocol: "managed-execution/v1", Commands: commands,
		Schemas:    []string{"haepari.envelope/v1", "haepari.job/v1", "haepari.resource/v1", "haepari.result/v1", "haepari.run-event/v1", "haepari.recovery-plan/v1", "haepari.artifact-manifest/v1", "haepari.artifact-page/v1", "haepari.purge-receipt/v1", "haepari.evidence-recovery-receipt/v1"},
		ErrorExits: errors, VerdictExits: map[managed.Verdict]int{managed.Pass: 0, managed.Fail: 1, managed.Incomplete: 3, managed.Cancelled: 4},
		Enums: map[string][]string{
			"job_state":             {"queued", "preparing", "ready", "running", "awaiting_manual", "cancelling", "collecting", "cleaning", "settling", "reconciling", "finished"},
			"availability":          {"available", "paused", "quarantined", "recovering"},
			"workload_outcome":      {"pass", "fail", "cancelled", "timed_out", "inconclusive"},
			"evidence_completeness": {"complete", "partial", "missing", "corrupt", "unobserved"},
			"cleanup_outcome":       {"verified", "failed", "unknown", "not_needed"},
			"retry":                 {"none", "retry_same_key", "reconcile"},
		},
		Cursors:          map[string]string{"events": "global monotonic integer scoped to one Job; retained ownership rejects foreign cursors before unavailable results; explicit prefix retention returns resync", "lists": "opaque scoped continuation; not a snapshot across pages", "logs": "exact byte offset plus file identity; a gap is explicit", "artifacts": "exact byte offset with signed file-incarnation token; export verifies full size and SHA-256"},
		Limits:           map[string]int64{"artifact_page_bytes": 64 << 10, "log_bytes": 256 << 20, "artifact_bytes_per_collection": 1 << 30, "artifacts_per_collection": 1000, "caller_wait_seconds": 86400},
		Canonicalization: "closed schemas reject duplicate and unknown fields; normalized POSIX relative paths; lowercase SHA-256; canonical typed admission JSON with ordered arrays and sorted map keys",
	})
}
