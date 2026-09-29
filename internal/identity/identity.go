package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
)

var Version = "0.1.0-dev"

type Info struct {
	Version          string `json:"version"`
	GoVersion        string `json:"go_version"`
	Platform         string `json:"platform"`
	SourceRevision   string `json:"source_revision,omitempty"`
	SourceModified   bool   `json:"source_modified"`
	ExecutableSHA256 string `json:"executable_sha256"`
}

func Read() (Info, error) {
	path, err := os.Executable()
	if err != nil {
		return Info{}, fmt.Errorf("find executable: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return Info{}, fmt.Errorf("open executable: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return Info{}, fmt.Errorf("hash executable: %w", err)
	}
	info := Info{
		Version:          Version,
		GoVersion:        runtime.Version(),
		Platform:         runtime.GOOS + "/" + runtime.GOARCH,
		ExecutableSHA256: "sha256:" + hex.EncodeToString(hash.Sum(nil)),
	}
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				info.SourceRevision = setting.Value
			case "vcs.modified":
				info.SourceModified = setting.Value == "true"
			}
		}
	}
	return info, nil
}
