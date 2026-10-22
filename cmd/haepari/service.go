package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/irootkernel/haepari/internal/client"
	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/state"
	"golang.org/x/sys/unix"
)

type developmentServicePlan struct {
	GlobalLimit         int    `json:"global_limit,omitempty"`
	HostMACOSGuestLimit int    `json:"host_macos_guest_limit,omitempty"`
	Schema              string `json:"schema"`
	Instance            string `json:"instance"`
	Label               string `json:"label"`
	Domain              string `json:"domain"`
	OwnerUID            int    `json:"owner_uid"`
	Binary              string `json:"binary"`
	BinarySHA256        string `json:"binary_sha256"`
	Root                string `json:"root"`
	PlistPath           string `json:"plist_path"`
	Plist               string `json:"plist"`
}

const (
	devServicePlanSchemaV1    = "haepari.development-service-plan/v1"
	devServicePlanSchemaV2    = "haepari.development-service-plan/v2"
	releaseServicePlanSchema  = "haepari.release-service-plan/v1"
	devServiceOwnerSchema     = "haepari.development-service-owner/v1"
	releaseServiceOwnerSchema = "haepari.release-service-owner/v1"
)

// serviceLabel keeps the isolated development prefix distinct from the release
// identity; a release plan never carries the dev label and vice versa.
func serviceLabel(schema, instance string) string {
	if schema == releaseServicePlanSchema {
		return "io.rootkernel.haepari." + instance
	}
	return "io.rootkernel.haepari.dev." + instance
}

func serviceOwnerSchema(schema string) string {
	if schema == releaseServicePlanSchema {
		return releaseServiceOwnerSchema
	}
	return devServiceOwnerSchema
}

var developmentInstanceID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

type developmentServiceOwner struct {
	Schema   string `json:"schema"`
	Instance string `json:"instance"`
	OwnerUID int    `json:"owner_uid"`
	Root     string `json:"root"`
}

func developmentRootOwner(p developmentServicePlan, create bool) error {
	path := filepath.Join(p.Root, ".development-service.json")
	expected := developmentServiceOwner{serviceOwnerSchema(p.Schema), p.Instance, p.OwnerUID, p.Root}
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err == nil {
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		raw, err := io.ReadAll(io.LimitReader(f, 4097))
		var owner developmentServiceOwner
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err != nil || len(raw) > 4096 || !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || st.Uid != uint32(p.OwnerUID) || st.Nlink != 1 || managed.ValidateUniqueJSONKeys(raw) != nil || d.Decode(&owner) != nil || owner != expected {
			return managed.ErrConflict
		}
		if d.Decode(new(any)) != io.EOF {
			return managed.ErrConflict
		}
		return nil
	}
	if !create || !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(p.Root)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return managed.ErrConflict
	}
	raw, _ := json.Marshal(expected)
	f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func plistString(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return "<string>" + b.String() + "</string>"
}

func planDevelopmentService(instance, binary, rootPath string) (developmentServicePlan, error) {
	return planManagedService(instance, binary, rootPath, 1, 1, false, false)
}
func planDevelopmentServiceLimits(instance, binary, rootPath string, globalLimit, hostLimit int, explicit bool) (developmentServicePlan, error) {
	return planManagedService(instance, binary, rootPath, globalLimit, hostLimit, explicit, false)
}
func planReleaseService(instance, binary, rootPath string, globalLimit, hostLimit int) (developmentServicePlan, error) {
	return planManagedService(instance, binary, rootPath, globalLimit, hostLimit, true, true)
}
func planManagedService(instance, binary, rootPath string, globalLimit, hostLimit int, explicit, release bool) (developmentServicePlan, error) {
	p := developmentServicePlan{}
	if globalLimit < 1 || hostLimit < 1 || hostLimit > 2 {
		return p, managed.ErrInvalid
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" || !developmentInstanceID.MatchString(instance) || !filepath.IsAbs(binary) || filepath.Clean(binary) != binary || !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != rootPath {
		return p, managed.ErrInvalid
	}
	root, err := state.Resolve(rootPath)
	if err != nil {
		return p, err
	}
	defaultRoot, err := state.Resolve("")
	if err != nil {
		return p, err
	}
	if root.Path == defaultRoot.Path {
		return p, managed.ErrInvalid
	}
	info, err := os.Lstat(binary)
	if err != nil {
		return p, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || st.Uid != uint32(os.Geteuid()) {
		return p, managed.ErrInvalid
	}
	f, err := os.Open(binary)
	if err != nil {
		return p, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return p, managed.ErrConflict
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return p, err
	}
	after, err := f.Stat()
	if err != nil || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return p, managed.ErrConflict
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p, err
	}
	p = developmentServicePlan{Schema: devServicePlanSchemaV1, Instance: instance, Label: "io.rootkernel.haepari.dev." + instance, Domain: "gui/" + strconv.Itoa(os.Geteuid()), OwnerUID: os.Geteuid(), Binary: binary, BinarySHA256: "sha256:" + hex.EncodeToString(h.Sum(nil)), Root: root.Path}
	if release {
		p.Schema, p.Label = releaseServicePlanSchema, serviceLabel(releaseServicePlanSchema, instance)
	}
	p.PlistPath = filepath.Join(home, "Library", "LaunchAgents", p.Label+".plist")
	limitArguments := ""
	if explicit {
		if !release {
			p.Schema = devServicePlanSchemaV2
		}
		p.GlobalLimit, p.HostMACOSGuestLimit = globalLimit, hostLimit
		limitArguments = plistString("--global-limit") + plistString(strconv.Itoa(globalLimit)) + plistString("--host-macos-guest-limit") + plistString(strconv.Itoa(hostLimit))
	}
	p.Plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key>` + plistString(p.Label) + `
<key>ProgramArguments</key><array>` + plistString(p.Binary) + plistString("daemon") + plistString("run") + plistString("--backend") + plistString("local") + limitArguments + `</array>
<key>EnvironmentVariables</key><dict><key>HAEPARI_HOME</key>` + plistString(p.Root) + `<key>PATH</key><string>/usr/bin:/bin</string></dict>
<key>LimitLoadToSessionType</key><string>Aqua</string>
<key>AbandonProcessGroup</key><true/>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
<key>ThrottleInterval</key><integer>1</integer>
<key>StandardOutPath</key>` + plistString(filepath.Join(p.Root, "service.stdout.log")) + `
<key>StandardErrorPath</key>` + plistString(filepath.Join(p.Root, "service.stderr.log")) + `
</dict></plist>
`
	return p, nil
}

func readDevelopmentServicePlan(path string, verifyBinary bool) (developmentServicePlan, error) {
	var p developmentServicePlan
	raw, err := readSpec(path)
	if err != nil {
		return p, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if managed.ValidateUniqueJSONKeys(raw) != nil || d.Decode(&p) != nil {
		return p, managed.ErrInvalid
	}
	var tail any
	if d.Decode(&tail) != io.EOF {
		return p, managed.ErrInvalid
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal(raw, &shape) != nil {
		return p, managed.ErrInvalid
	}
	_, hasGlobal := shape["global_limit"]
	_, hasHost := shape["host_macos_guest_limit"]
	switch p.Schema {
	case devServicePlanSchemaV1:
		if hasGlobal || hasHost {
			return p, managed.ErrInvalid
		}
	case devServicePlanSchemaV2:
		if !hasGlobal || !hasHost || p.GlobalLimit < 1 || p.HostMACOSGuestLimit < 1 || p.HostMACOSGuestLimit > 2 {
			return p, managed.ErrInvalid
		}
	case releaseServicePlanSchema:
		if !hasGlobal || !hasHost || p.GlobalLimit < 1 || p.HostMACOSGuestLimit < 1 || p.HostMACOSGuestLimit > 2 {
			return p, managed.ErrInvalid
		}
	default:
		return p, managed.ErrInvalid
	}
	if verifyBinary {
		current, err := recreateDevelopmentServicePlan(p)
		if err != nil {
			return p, err
		}
		if current != p {
			return p, managed.ErrConflict
		}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return p, err
		}
		if p.OwnerUID != os.Geteuid() || p.Domain != "gui/"+strconv.Itoa(os.Geteuid()) || p.Label != serviceLabel(p.Schema, p.Instance) || !developmentInstanceID.MatchString(p.Instance) || p.PlistPath != filepath.Join(home, "Library", "LaunchAgents", p.Label+".plist") {
			return p, managed.ErrInvalid
		}
	}
	return p, nil
}

func recreateDevelopmentServicePlan(p developmentServicePlan) (developmentServicePlan, error) {
	switch p.Schema {
	case devServicePlanSchemaV1:
		return planDevelopmentService(p.Instance, p.Binary, p.Root)
	case devServicePlanSchemaV2:
		return planDevelopmentServiceLimits(p.Instance, p.Binary, p.Root, p.GlobalLimit, p.HostMACOSGuestLimit, true)
	case releaseServicePlanSchema:
		return planReleaseService(p.Instance, p.Binary, p.Root, p.GlobalLimit, p.HostMACOSGuestLimit)
	default:
		return developmentServicePlan{}, managed.ErrInvalid
	}
}

func launchctlService(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 4096 {
			output = output[:4096]
		}
		return fmt.Errorf("launchctl %s: %w: %s", args[0], err, output)
	}
	return nil
}

func installDevelopmentService(p developmentServicePlan) error {
	current, err := recreateDevelopmentServicePlan(p)
	if err != nil {
		return err
	}
	if current != p {
		return managed.ErrConflict
	}
	root, err := state.Resolve(p.Root)
	if err != nil {
		return err
	}
	if err = root.Prepare(); err != nil {
		return err
	}
	if err = developmentRootOwner(p, true); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p.PlistPath), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(p.PlistPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(p.Plist)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	// A failed or uncertain bootstrap preserves the exact plist and state for
	// native inspection. It never overwrites another service or rolls it out.
	return launchctlService("bootstrap", p.Domain, p.PlistPath)
}

func uninstallDevelopmentService(p developmentServicePlan) error {
	if err := developmentRootOwner(p, false); err != nil {
		return err
	}
	info, err := os.Lstat(p.PlistPath)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Uid != uint32(os.Geteuid()) {
		return managed.ErrInvalid
	}
	raw, err := os.ReadFile(p.PlistPath)
	if err != nil {
		return err
	}
	if string(raw) != p.Plist {
		return managed.ErrConflict
	}
	root, err := state.Resolve(p.Root)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.Connect(ctx, root)
	if err == nil {
		defer c.Close()
		page, _, err := c.Resources(ctx, "", 100)
		if err != nil {
			return err
		}
		for _, r := range page.Resources {
			if r.Occupancy != 0 {
				return managed.ErrBlocked
			}
		}
		if page.More {
			return managed.ErrBlocked
		}
	}
	if err = launchctlService("bootout", p.Domain+"/"+p.Label); err != nil {
		return err
	}
	// Remove only the exact owned regular file; never remove the state root or
	// its retained runner/evidence, even when the service was already stopped.
	now, err := os.Lstat(p.PlistPath)
	if err != nil || !os.SameFile(info, now) {
		return managed.ErrConflict
	}
	return os.Remove(p.PlistPath)
}

func serviceCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: haepari service plan [--release] --instance <id> --binary <absolute-path> --root <private-root> --json | install/uninstall --plan-file <json> --json")
		return 2
	}
	flags := flag.NewFlagSet("service "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	instance := flags.String("instance", "", "isolated development instance")
	binary := flags.String("binary", "", "pinned candidate executable")
	root := flags.String("root", "", "isolated private state root")
	file := flags.String("plan-file", "", "exact saved plan")
	outputJSON := flags.Bool("json", false, "machine-readable response")
	globalLimit := flags.Int("global-limit", 1, "positive global execution limit")
	hostLimit := flags.Int("host-macos-guest-limit", 1, "compatibility setting (1 or 2); does not limit physical VM count")
	release := flags.Bool("release", false, "plan the explicit user release service identity")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !*outputJSON {
		return 2
	}
	explicitLimits := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "global-limit" || f.Name == "host-macos-guest-limit" {
			explicitLimits = true
		}
	})
	var p developmentServicePlan
	var err error
	switch args[0] {
	case "plan":
		if *file != "" {
			return 2
		}
		if *release {
			p, err = planReleaseService(*instance, *binary, *root, *globalLimit, *hostLimit)
		} else {
			p, err = planDevelopmentServiceLimits(*instance, *binary, *root, *globalLimit, *hostLimit, explicitLimits)
		}
	case "install", "uninstall":
		if *release || explicitLimits || *file == "" || *instance != "" || *binary != "" || *root != "" {
			return 2
		}
		p, err = readDevelopmentServicePlan(*file, args[0] == "install")
		if err == nil {
			if args[0] == "install" {
				err = installDevelopmentService(p)
			} else {
				err = uninstallDevelopmentService(p)
			}
		}
	default:
		fmt.Fprintln(stderr, "unsupported service action")
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		code := managed.StorageUnavailable
		if errors.Is(err, managed.ErrConflict) {
			code = managed.Conflict
		} else if errors.Is(err, managed.ErrBlocked) {
			code = managed.RecoveryBlocked
		} else if errors.Is(err, managed.ErrInvalid) || errors.Is(err, state.ErrInvalidRoot) || os.IsNotExist(err) {
			code = managed.InvalidRequest
		}
		issue := &managed.MachineError{Code: code, Message: "development service plan, ownership or native operation could not be verified; preserve state and inspect the exact instance", Retry: "reconcile"}
		if code == managed.InvalidRequest || code == managed.Conflict {
			issue.Retry = "none"
		}
		if json.NewEncoder(stdout).Encode(managed.Envelope{Schema: "haepari.envelope/v1", Operation: "service." + args[0], OK: false, Error: issue}) != nil {
			return 6
		}
		exit, _ := managed.ExitCode(code)
		return exit
	}
	return writeJSON(stdout, stderr, "service."+args[0], p)
}
