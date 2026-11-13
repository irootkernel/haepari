package runner

import (
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
)

const PersistentRunnerRequestSchema = managed.PersistentRunnerRequestSchema

// Versions 1/2 are frozen. Versions 3/4 add preparation identity to their
// respective ordinary/absence-aware guest protocols.
func guestVersion(schema, family string) int {
	for n, suffix := range []string{"v1", "v2", "v3", "v4"} {
		if schema == "haepari."+family+"/"+suffix {
			return n + 1
		}
	}
	return 0
}

func GuestUsesPersistentIdentity(q GuestRequest) bool {
	return guestVersion(q.Schema, "guest-request") >= 3
}

func GuestAbsenceAware(q GuestRequest) bool {
	v := guestVersion(q.Schema, "guest-request")
	return v == 2 || v == 4
}

func GuestRecoveryIsReboot(schema string) bool {
	v := guestVersion(schema, "guest-recovery-request")
	return v == 2 || v == 4
}

func GuestAttestationIsReboot(schema string) bool {
	v := guestVersion(schema, "guest-attestation")
	return v == 2 || v == 4
}

func guestSchema(family string, version int) string {
	if version < 1 || version > 4 {
		return ""
	}
	return "haepari." + family + "/v" + string(rune('0'+version))
}

func GuestRecoveryRequestSchema(q GuestRequest, reboot bool) string {
	v := 1
	if GuestUsesPersistentIdentity(q) {
		v = 3
	}
	if reboot {
		v++
	}
	return guestSchema("guest-recovery-request", v)
}

func GuestAttestationSchema(q GuestRequest, reboot bool) string {
	return strings.Replace(GuestRecoveryRequestSchema(q, reboot), "guest-recovery-request", "guest-attestation", 1)
}

func guestControlSchema(q GuestRequest) string {
	return guestSchema("guest-control", guestVersion(q.Schema, "guest-request"))
}

func legacyRetainedPath(p *managed.RetainedPath) bool {
	return p == nil || p.Persistent == (platform.FileIdentity{}) && p.ObservedBoot == ""
}

func validGuestIdentityVersion(q GuestRequest) bool {
	v := guestVersion(q.Schema, "guest-request")
	if v == 0 || !validPreparationVersion(q.Preparation) {
		return false
	}
	if v <= 2 {
		return q.Preparation == nil && legacyRetainedPath(q.Root) &&
			q.Request.LogIdentity == (platform.FileIdentity{}) && q.Request.Schema != PersistentRunnerRequestSchema
	}
	return true
}

func validControlIdentityVersion(c GuestControl) bool {
	v := guestVersion(c.Schema, "guest-control")
	if !validPreparationVersion(c.Preparation) {
		return false
	}
	if v <= 2 {
		return c.Preparation == nil && legacyRetainedPath(c.Root) && legacyRetainedPath(c.WorkspaceRoot) &&
			(c.NativeRequest == nil || c.NativeRequest.LogIdentity == (platform.FileIdentity{}) && c.NativeRequest.Schema != PersistentRunnerRequestSchema)
	}
	return true
}

func validReplyIdentityVersion(r GuestReply) bool {
	v := guestVersion(r.Schema, "guest-reply")
	if !validPreparationVersion(r.Preparation) {
		return false
	}
	if v <= 2 {
		return r.Preparation == nil && legacyRetainedPath(r.Root) &&
			(r.Request == nil || r.Request.LogIdentity == (platform.FileIdentity{}) && r.Request.Schema != PersistentRunnerRequestSchema) &&
			(r.Control == nil || validControlIdentityVersion(*r.Control))
	}
	return r.Control == nil || validControlIdentityVersion(*r.Control)
}

func GuestRecoveryReplySchema(q GuestRecoveryRequest) string {
	return guestSchema("guest-recovery-reply", guestVersion(q.Schema, "guest-recovery-request"))
}

func legacyRecoveryBasis(b *GuestRecoveryBasis) bool {
	return b == nil || legacyRetainedPath(&b.Root) && legacyRetainedPath(&b.Control) &&
		legacyRetainedPath(b.WorkspaceRoot) && b.Request.LogIdentity == (platform.FileIdentity{}) &&
		b.Request.Schema != PersistentRunnerRequestSchema
}

// Structural decoding stays compatible with historical schema headers; the
// execution entrypoint separately validates known protocol versions.
func legacyGuestIdentityFields(q GuestRequest) bool {
	return q.Preparation == nil && legacyRetainedPath(q.Root) &&
		q.Request.LogIdentity == (platform.FileIdentity{}) && q.Request.Schema != PersistentRunnerRequestSchema
}

func validRecoveryRequestFields(q GuestRecoveryRequest) bool {
	if guestVersion(q.Schema, "guest-recovery-request") >= 3 {
		return validGuestIdentityVersion(q.Guest) && GuestUsesPersistentIdentity(q.Guest)
	}
	return legacyGuestIdentityFields(q.Guest) && legacyRecoveryBasis(q.Basis) &&
		(q.Attestation == nil || legacyRecoveryBasis(&q.Attestation.Basis) && legacyGuestIdentityFields(q.Attestation.Guest))
}

func validRecoveryRequestVersion(q GuestRecoveryRequest) bool {
	v := guestVersion(q.Schema, "guest-recovery-request")
	return v != 0 && validGuestIdentityVersion(q.Guest) && validRecoveryRequestFields(q) &&
		(v >= 3) == GuestUsesPersistentIdentity(q.Guest)
}

func validRecoveryReplyVersion(r GuestRecoveryReply) bool {
	v := guestVersion(r.Schema, "guest-recovery-reply")
	g := guestVersion(r.Guest.Schema, "guest-reply")
	if !validReplyIdentityVersion(r.Guest) {
		return false
	}
	if v >= 3 {
		return g >= 3
	}
	return g < 3 && legacyRecoveryBasis(r.Basis) &&
		(r.Attestation == nil || legacyRecoveryBasis(&r.Attestation.Basis) && legacyGuestIdentityFields(r.Attestation.Guest)) &&
		(r.CompletedCleanup == nil || legacyRetainedPath(&r.CompletedCleanup.ControlInode) && validControlIdentityVersion(r.CompletedCleanup.Control))
}

func guestCleanupSchema(guest GuestRequest, absent bool) string {
	v := 1
	if GuestUsesPersistentIdentity(guest) {
		v = 3
	}
	if absent {
		v++
	}
	return guestSchema("guest-attested-cleanup", v)
}

func guestTransportBindingSchema(q GuestRequest) string {
	if GuestUsesPersistentIdentity(q) {
		return "haepari.guest-transport-binding/v2"
	}
	return "haepari.guest-transport-binding/v1"
}

func validateGuestRequest(request Request, root string) error {
	// Preparation has not opened its native log yet. Validate the ordinary
	// request constraints without inventing a persistent log identity.
	if request.LogInode == 0 {
		if request.Schema == PersistentRunnerRequestSchema {
			if request.LogDevice != 0 || request.LogIdentity != (platform.FileIdentity{}) {
				return ErrInvalid
			}
			request.Schema = managed.RunnerSchema("request")
		}
		request.LogInode = 1
	}
	return request.Validate(root)
}

func guestBindsWorkspace(q GuestRequest) bool {
	return GuestAbsenceAware(q) || GuestUsesPersistentIdentity(q)
}

func GuestClassifiedDiagnostics(q GuestRequest) bool {
	return guestVersion(q.Schema, "guest-request") >= 2
}
