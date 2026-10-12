package managed

// DoctorReport is a point-in-time prerequisite observation, not a lease or a
// reusable-resource proof. Admission and dispatch retain their own fences.
type DoctorReport struct {
	Schema         string          `json:"schema"`
	InstallationID string          `json:"installation_id"`
	ResourceID     string          `json:"resource_id"`
	Resource       *ResourceStatus `json:"resource,omitempty"`
	ObservedAt     string          `json:"observed_at"`
	Ready          bool            `json:"ready"`
	Gaps           []DoctorGap     `json:"gaps"`
}

type DoctorGap struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
