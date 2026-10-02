package managed

func Capabilities() []string {
	return []string{"managed-execution/v1", "durable-observation/v1", "advisory-lock/v1", "managed-recovery/v1", "artifact-delivery/v1", "evidence-retention/v1"}
}
