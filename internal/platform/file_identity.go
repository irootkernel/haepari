package platform

import "encoding/hex"

// FileIdentity is observed from an open descriptor, never inferred from a path.
// A zero value is historical/unsupported evidence, not a recoverable identity.
type FileIdentity struct {
	VolumeUUID     string `json:"volume_uuid"`
	FileID         uint64 `json:"file_id"`
	CreatedSeconds int64  `json:"created_seconds"`
	CreatedNanos   int64  `json:"created_nanos"`
}

func (i FileIdentity) Valid() bool {
	_, err := hex.DecodeString(i.VolumeUUID)
	return err == nil && len(i.VolumeUUID) == 32 && i.VolumeUUID != "00000000000000000000000000000000" && i.FileID != 0 && i.CreatedSeconds > 0 && i.CreatedNanos >= 0 && i.CreatedNanos < 1000000000
}
