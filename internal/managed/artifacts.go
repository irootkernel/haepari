package managed

// Artifact delivery is a finished-Job view. Supplementary recovery files use
// separate paths and never replace an immutable result's original inventory.
type ArtifactManifest struct {
	Schema        string                  `json:"schema"`
	JobID         string                  `json:"job_id"`
	Result        Result                  `json:"result"`
	Files         []DeliveryFile          `json:"files"`
	Supplementary []PublicRecoveryOutcome `json:"supplementary"`
}

// Frozen public v1 projection. Private guest attestation and native inventories
// remain recovery authority; native files use the existing delivery file paths.
type PublicRecoveryOutcome struct {
	JobID        string           `json:"job_id"`
	ResultDigest string           `json:"result_digest"`
	Stage        string           `json:"stage"`
	ArtifactRoot string           `json:"artifact_root,omitempty"`
	Artifacts    []ResultArtifact `json:"artifacts"`
	Gap          string           `json:"gap,omitempty"`
}

type DeliveryFile struct {
	ResultArtifact
	PageToken string `json:"page_token,omitempty"`
}

type ArtifactPage struct {
	Schema     string         `json:"schema"`
	JobID      string         `json:"job_id"`
	File       ResultArtifact `json:"file"`
	Offset     int64          `json:"offset"`
	NextOffset int64          `json:"next_offset"`
	Data       []byte         `json:"data"`
	More       bool           `json:"more"`
}

func ValidArtifactPath(path string) bool { return path != "." && relativePath(path) }
