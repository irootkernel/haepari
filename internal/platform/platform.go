package platform

import "fmt"

const DefaultBackend = "local"

func CompiledBackends() []string {
	return []string{DefaultBackend}
}

func Validate(goos, goarch, backend string) error {
	if goos != "darwin" || goarch != "arm64" {
		return fmt.Errorf("unsupported platform %s/%s: qualified bootstrap target is darwin/arm64", goos, goarch)
	}
	if backend != DefaultBackend {
		return fmt.Errorf("unsupported backend %q: only local bootstrap is available", backend)
	}
	return nil
}
