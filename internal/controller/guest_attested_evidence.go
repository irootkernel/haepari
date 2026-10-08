package controller

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/runner"
	"github.com/irootkernel/haepari/internal/utm"
)

func (b vmEvidenceBackend) observeAttested(q runner.GuestRecoveryRequest, id managed.LocalIdentity, action string) (managed.RetainedPath, error) {
	if q.Guest.Request.Identity != id || id.ResourceID != b.resourceID {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	if action == "dispose" {
		snap, err := b.s.ledger.ResourceRecoverySnapshot(b.resourceID)
		allowed := false
		if err == nil && snap.EvidenceIntent != nil && snap.EvidenceIntent.Action == "recollect" && snap.EvidenceReceipt != nil {
			for _, o := range snap.EvidenceReceipt.Jobs {
				if o.JobID == id.JobID && o.Stage == "collected" && len(o.GuestArtifacts) == 7 {
					allowed = true
				}
			}
		}
		if !allowed {
			return managed.RetainedPath{}, errVMNativeTermination
		} // no attested evidence-loss disposal
	}
	q.Operation = "state"
	q.Deadline = time.Now().Add(5 * time.Second)
	reply, err := b.s.vmRecoveryCall(q)
	if err != nil {
		q.Operation = "health"
		reply, err = b.s.vmRecoveryCall(q)
		if err != nil || !reply.Reusable {
			return managed.RetainedPath{}, managed.ErrBlocked
		}
	}
	if reply.Basis == nil || reply.Basis.Root != q.Attestation.Basis.Root {
		return managed.RetainedPath{}, managed.ErrBlocked
	}
	return reply.Basis.Root, nil
}
func (b vmEvidenceBackend) attestedIntent(j managed.EvidenceRecoveryJob) (managed.EvidenceRecoveryIntent, error) {
	snap, err := b.s.ledger.ResourceRecoverySnapshot(b.resourceID)
	if err != nil || snap.EvidenceIntent == nil || snap.EvidenceIntent.Action != "recollect" {
		return managed.EvidenceRecoveryIntent{}, managed.ErrBlocked
	}
	for _, owned := range snap.EvidenceIntent.Jobs {
		if managed.RecoveryDigest(owned) == managed.RecoveryDigest(j) {
			return *snap.EvidenceIntent, nil
		}
	}
	return managed.EvidenceRecoveryIntent{}, managed.ErrBlocked
}
func (b vmEvidenceBackend) collectAttested(q runner.GuestRecoveryRequest, j managed.EvidenceRecoveryJob, destination string) collectionAttempt {
	if _, err := b.attestedIntent(j); err != nil {
		return collectionAttempt{}
	}
	a := collectionAttempt{complete: true}
	q.Deadline = j.CollectDeadline
	q.Operation = "file"
	call := func(guest runner.GuestRequest) (runner.GuestReply, error) {
		request := q
		request.Guest = guest
		reply, err := b.s.vmRecoveryCall(request)
		return reply.Guest, err
	}
	for _, dir := range []string{destination, destination + "-native"} {
		if err := os.Mkdir(dir, 0700); err != nil && (!errors.Is(err, os.ErrExist) || privateVMDirectory(dir) != nil) {
			return collectionAttempt{}
		}
	}
	var total int64
	for _, name := range j.Outputs {
		v, err := b.s.collectVMFileUsing(q.Guest, name, filepath.Join(destination, name), j.CollectDeadline, (1<<30)-total, call)
		if err != nil {
			a.complete = false
			return a
		}
		a.artifacts = append(a.artifacts, v)
		total += v.SizeBytes
	}
	for _, name := range []string{"output.log", "guest.json", "request.json", "intent.json", "start.json", "outcome.json", "exit.json"} {
		guest := q.Guest
		guest.Area = "receipt"
		if name == "output.log" {
			guest.Area = "log"
		}
		limit, valid := managed.GuestRecoveryArtifactLimit(name, q.Attestation.Basis.Request.LogLimitBytes)
		if !valid || j.GuestLogLimitBytes != q.Attestation.Basis.Request.LogLimitBytes {
			a.complete = false
			return a
		}
		v, err := b.s.collectVMFileUsing(guest, name, filepath.Join(destination+"-native", name), j.CollectDeadline, limit, call)
		if err != nil || name != "output.log" && "sha256:"+v.SHA256 != q.Attestation.Basis.Digests[name] || name == "output.log" && q.Attestation.Basis.Observation.Exit.Log != nil && v != q.Attestation.Basis.Observation.Exit.Log.Artifact {
			a.complete = false
			return a
		}
		a.guestArtifacts = append(a.guestArtifacts, v)
	}
	return a
}
func (b vmEvidenceBackend) cleanupAttested(q runner.GuestRecoveryRequest, j managed.EvidenceRecoveryJob) error {
	intent, err := b.attestedIntent(j)
	if err != nil {
		return err
	}
	_, receipt, _, err := b.s.ledger.EvidenceRecovery(intent.MutationKey, intent.ClientDigest)
	if err != nil || receipt == nil {
		return managed.ErrBlocked
	}
	var collected *managed.EvidenceRecoveryOutcome
	for i := range receipt.Jobs {
		if receipt.Jobs[i].JobID == j.JobID && receipt.Jobs[i].Stage == "collected" && len(receipt.Jobs[i].GuestArtifacts) == 7 {
			collected = &receipt.Jobs[i]
		}
	}
	if collected == nil {
		return managed.ErrBlocked
	}
	// Revalidate all supplementary bytes immediately before the effect as well
	// as at the durable collected-stage boundary. A reply loss cannot authorize
	// a changed host inventory or a new cleanup key/deadline.
	for _, set := range []struct {
		root  string
		files []managed.ResultArtifact
	}{{collected.ArtifactRoot, collected.Artifacts}, {collected.ArtifactRoot + "-native", collected.GuestArtifacts}} {
		for _, a := range set.files {
			f, e := openOwnedEvidence(b.s.root.Path, j.JobID, set.root+"/"+a.Path)
			if e != nil {
				return e
			}
			_, e = checkedArtifactUntil(f, &a, a.Path, j.CleanupDeadline)
			f.Close()
			if e != nil {
				return e
			}
		}
	}
	q.Deadline = j.CleanupDeadline
	q.Operation = "health"
	if reply, e := b.s.vmRecoveryCall(q); e == nil && reply.Reusable {
		return nil
	}
	q.Operation = "cleanup"
	q.MutationKey = intent.MutationKey
	q.CollectionDigest = managed.RecoveryDigest(collected)
	reply, err := b.s.vmRecoveryCall(q)
	if err != nil {
		if !errors.Is(err, utm.ErrGuestUncertain) {
			return err
		}
		// A transport observer can lose the completed cleanup reply. Reconcile
		// by observation under the saved deadline; never extend or resubmit it.
		q.Operation = "health"
		observed, e := b.s.vmRecoveryCall(q)
		if e == nil && observed.Reusable {
			return nil
		}
		return runner.ErrUnproven
	}
	if !reply.Reusable {
		return managed.ErrBlocked
	}
	q.Operation = "health"
	reply, err = b.s.vmRecoveryCall(q)
	if err != nil || !reply.Reusable {
		return managed.ErrBlocked
	}
	return nil
}

// Decode the recorded request before recovery execution, including after
// restart. The existing closed-shape guest protocol is never used as a bypass.
func decodeAttestationRequest(raw json.RawMessage) (runner.GuestRecoveryRequest, error) {
	q, err := runner.DecodeGuestRecoveryRequest(raw)
	if err != nil || q.Basis == nil || q.Evidence == nil || q.Operation != "attest" {
		return q, managed.ErrBlocked
	}
	return q, nil
}
