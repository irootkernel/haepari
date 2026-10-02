package managed

// Runner vocabulary and workload defaults belong to the domain, consumed by
// both the receipt writer and ledger importer without a dependency cycle.
const DefaultWorkloadPATH = "/usr/bin:/bin"

type RunnerBinding struct {
	Identity      LocalIdentity `json:"identity"`
	RequestDigest string        `json:"request_digest"`
}

func RunnerSchema(kind string) string {
	switch kind {
	case "request", "intent", "start", "outcome", "exit", "cancel":
		return "haepari.runner-" + kind + "/v1"
	default:
		return ""
	}
}
