package managed

const GuestRecoveryReceiptBytes = 2 << 20
const GuestRecoveryLogBytes int64 = 256 << 20

// Native recovery evidence has its own bounds, independent of required outputs.
// A zero log limit retains the historical runner default.
func GuestRecoveryArtifactLimit(path string, logLimit int64) (int64, bool) {
	if logLimit < 0 || logLimit > GuestRecoveryLogBytes {
		return 0, false
	}
	switch path {
	case "output.log":
		if logLimit == 0 {
			logLimit = GuestRecoveryLogBytes
		}
		return logLimit, true
	case "guest.json", "request.json", "intent.json", "start.json", "outcome.json", "exit.json":
		return GuestRecoveryReceiptBytes, true
	}
	return 0, false
}
