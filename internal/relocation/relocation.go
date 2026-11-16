package relocation

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/platform"
	"golang.org/x/sys/unix"
)

func NewPlan(source, target ServicePlan, backup string, e Environment) (_ Plan, final error) {
	defer func() {
		if final != nil {
			final = fail("plan_observation", final)
		}
	}()
	p := Plan{Schema: PlanSchema, SourcePlan: source, TargetPlan: target, BackupDir: backup}
	if err := validatePairs(p, e); err != nil {
		return Plan{}, err
	}
	root, err := openDirectory(source.Root)
	if err != nil {
		return Plan{}, err
	}
	defer root.close()
	if root.record.Mode&0077 != 0 || root.record.OwnerUID != uint32(source.OwnerUID) {
		return Plan{}, managed.ErrConflict
	}
	p.SourceRootIdentity = root.record
	release, err := acquire(root)
	if err != nil {
		return Plan{}, err
	}
	defer release()
	for _, bind := range []struct {
		path string
		out  *Directory
	}{{filepath.Dir(source.Root), &p.SourceParentIdentity}, {filepath.Dir(target.Root), &p.TargetParentIdentity}, {filepath.Dir(backup), &p.BackupParentIdentity}} {
		parent, err := openDirectory(bind.path)
		if err != nil {
			return Plan{}, err
		}
		*bind.out = parent.record
		parent.close()
	}
	if err = checkParents(p); err != nil {
		return Plan{}, err
	}
	for _, name := range []string{target.Root, backup} {
		if _, err = os.Lstat(name); !os.IsNotExist(err) {
			return Plan{}, managed.ErrConflict
		}
	}
	if _, err = ownerBytes(root, source); err != nil {
		return Plan{}, err
	}
	o, err := observe(root, source.Root, source)
	if err != nil {
		return Plan{}, err
	}
	defer func() {
		if err := o.db.Close(); final == nil && err != nil {
			final = fail("observation_cleanup", err)
		}
	}()
	p.InstallationID, p.LedgerSHA256, p.Files = o.installation, o.digest, o.files
	j, file, raw, err := readJournal(root)
	if err == nil {
		if !terminal(j) || j.Plan.InstallationID != p.InstallationID || j.Plan.SourceRootIdentity != p.SourceRootIdentity || j.Phase == "complete" && source.Root != j.Plan.TargetPlan.Root || j.Phase == "rolled_back" && source.Root != j.Plan.SourcePlan.Root {
			return Plan{}, managed.ErrConflict
		}
		if err = installationIdentity(root, j.Plan); err != nil {
			return Plan{}, err
		}
		if err = verifyHistory(root, j); err != nil {
			err = fail("history_chain", err)
			return Plan{}, err
		}
		p.Predecessor = &Predecessor{j.Plan.RelocationID, j.PlanSHA256, j.Sequence, Digest(raw), file.Identity, source.Root}
	} else if !journalAbsent(err) {
		return Plan{}, err
	} else if _, err = verifyHistoryNamespace(root, p.InstallationID); err != nil {
		return Plan{}, err
	}
	p.RelocationID, err = newID()
	if err != nil {
		return Plan{}, err
	}
	p.PlanSHA256 = planDigest(p)
	if err = validateFilePlan(p); err != nil {
		return Plan{}, err
	}
	return p, nil
}
func revalidate(root *held, current string, p Plan, j *Journal, e Environment) (*observation, error) {
	other := p.TargetPlan.Root
	if current == p.TargetPlan.Root {
		other = p.SourcePlan.Root
	}
	otherParent, _, guardErr := parentFor(p, other)
	if guardErr != nil {
		return nil, guardErr
	}
	guardErr = absent(otherParent, filepath.Base(other))
	otherParent.close()
	if guardErr != nil {
		return nil, guardErr
	}

	if err := validatePairs(p, e); err != nil {
		return nil, err
	}
	if err := checkParents(p); err != nil {
		return nil, err
	}
	if root.matches(unix.AT_FDCWD, current) != nil || root.record != p.SourceRootIdentity {
		return nil, managed.ErrConflict
	}
	o, err := observe(root, current, p.SourcePlan)
	if err != nil {
		return nil, err
	}
	if o.installation != p.InstallationID || o.digest != p.LedgerSHA256 {
		_ = o.db.Close()
		return nil, managed.ErrConflict
	}
	if err = inventoryMatches(root, current, p, j, o.files); err != nil {
		err = fail("retained_inventory", err)
		_ = o.db.Close()
		return nil, err
	}
	if e.ObservationOpened != nil {
		e.ObservationOpened(o.db)
	}
	return o, nil
}
func prepare(root *held, current string, p Plan, e Environment) (Journal, error) {
	raw, err := ownerBytes(root, p.SourcePlan)
	if err != nil {
		return Journal{}, err
	}
	var owner struct {
		Schema   string `json:"schema"`
		Instance string `json:"instance"`
		OwnerUID int    `json:"owner_uid"`
		Root     string `json:"root"`
	}
	if Decode(raw, &owner) != nil {
		return Journal{}, managed.ErrConflict
	}
	owner.Root = p.TargetPlan.Root
	j := Journal{Schema: JournalSchema, Plan: p, PlanSHA256: p.PlanSHA256, Sequence: 1, Phase: "prepared", BackupState: "not_created", OriginalOwner: raw, TargetOwner: canonical(owner)}
	parent, pp, err := parentFor(p, current)
	if err != nil {
		return Journal{}, err
	}
	defer parent.close()
	prior, file, old, err := readJournal(root)
	if err == nil {
		j.PredecessorArchive, err = archivePredecessor(root, current, p, prior, file, old, e)
		if err != nil {
			return Journal{}, err
		}
		e.boundary("before_successor_publication")
		err = publish(parent, pp, root, current, JournalName, canonical(j), &file, old, e)
	} else if journalAbsent(err) {
		if _, err = verifyHistoryNamespace(root, p.InstallationID); err != nil {
			return Journal{}, err
		}
		if p.Predecessor != nil {
			return Journal{}, managed.ErrConflict
		}
		err = publish(parent, pp, root, current, JournalName, canonical(j), nil, nil, e)
	}
	if err != nil {
		return Journal{}, err
	}
	e.boundary("journal_prepared")
	return j, nil
}
func renameRoot(root *held, p Plan, from, to string, e Environment) error {
	source, sp, err := parentFor(p, from)
	if err != nil {
		return err
	}
	defer source.close()
	target, tp, err := parentFor(p, to)
	if err != nil {
		return err
	}
	defer target.close()
	e.boundary("before_rename")
	if source.matches(unix.AT_FDCWD, sp) != nil || target.matches(unix.AT_FDCWD, tp) != nil || root.matches(source.fd(), filepath.Base(from)) != nil {
		return managed.ErrConflict
	}
	if err = absent(target, filepath.Base(to)); err != nil {
		return err
	}
	if err = platform.RenameExclusive(source.fd(), filepath.Base(from), target.fd(), filepath.Base(to)); err != nil {
		return err
	}
	e.boundary("rename_effect")
	return reconcileRename(root, p, from, to, e)
}

// Visibility after interruption does not prove either namespace sync completed.
func reconcileRename(root *held, p Plan, from, to string, e Environment) error {
	source, sp, err := parentFor(p, from)
	if err != nil {
		return err
	}
	defer source.close()
	target, tp, err := parentFor(p, to)
	if err != nil {
		return err
	}
	defer target.close()
	if root.matches(target.fd(), filepath.Base(to)) != nil || absent(source, filepath.Base(from)) != nil {
		return managed.ErrConflict
	}
	if err = syncDirectory(source, sp, e); err != nil {
		return err
	}
	if err = syncDirectory(target, tp, e); err != nil {
		return err
	}
	if root.matches(target.fd(), filepath.Base(to)) != nil {
		return managed.ErrConflict
	}
	e.boundary("rename_durable")
	return nil
}

// Operate continues only the exact original plan. Locks are never replaced or
// dropped during a directory move, a phase publication or the final receipt.
func Operate(p Plan, action string, e Environment) (_ Receipt, final error) {
	defer func() {
		if final != nil {
			final = fail(action+"_validation", final)
		}
	}()
	if action != "apply" && action != "status" && action != "rollback" {
		return Receipt{}, managed.ErrInvalid
	}
	if _, err := DecodePlan(canonical(p)); err != nil {
		return Receipt{}, err
	}
	if err := validateFilePlan(p); err != nil {
		return Receipt{}, err
	}
	if action == "status" {
		return status(p, e)
	}
	if err := validatePairs(p, e); err != nil {
		return Receipt{}, err
	}
	root, current, err := locate(p)
	if err != nil {
		return Receipt{}, err
	}
	defer root.close()
	release, err := acquire(root)
	if err != nil {
		return Receipt{}, err
	}
	defer release()
	if err = checkParents(p); err != nil {
		return Receipt{}, err
	}
	j, entry, raw, err := readJournal(root)
	if err != nil && !journalAbsent(err) {
		return Receipt{}, err
	}
	isNew := journalAbsent(err) || j.PlanSHA256 != p.PlanSHA256
	if isNew {
		if err == nil && !predecessorMatches(p, j, entry, raw, current) {
			if action == "status" {
				return archivedReceipt(root, j, p)
			}
			return Receipt{}, managed.ErrConflict
		}
		if action == "rollback" {
			return Receipt{}, managed.ErrConflict
		}
		if current != p.SourcePlan.Root {
			return Receipt{}, managed.ErrConflict
		}
		o, err := revalidate(root, current, p, nil, e)
		if err != nil {
			return Receipt{}, err
		}
		if err = o.db.Close(); err != nil {
			return Receipt{}, err
		}
		if _, err = os.Lstat(p.BackupDir); !os.IsNotExist(err) {
			return Receipt{}, managed.ErrConflict
		}
		if action == "status" {
			return Receipt{Schema: ReceiptSchema, RelocationID: p.RelocationID, PlanSHA256: p.PlanSHA256, InstallationID: p.InstallationID, Phase: "planned", SourceRoot: p.SourcePlan.Root, TargetRoot: p.TargetPlan.Root, CurrentRoot: current, BackupState: "not_created"}, nil
		}
		j, err = prepare(root, current, p, e)
		if err != nil {
			return Receipt{}, fail("prepared_journal", err)
		}
	} else if !bytes.Equal(canonical(j.Plan), canonical(p)) {
		return Receipt{}, managed.ErrConflict
	}
	// A visible journal may be the last unsynced publication of an interrupted
	// process. Finish its directory durability before effects or receipt reuse.
	if err = syncDirectory(root, current, e); err != nil {
		return Receipt{}, err
	}
	if action == "rollback" {
		return rollback(root, current, &j, e)
	}
	if j.Phase == "rolled_back" || j.Phase == "rollback_pending" {
		return Receipt{}, managed.ErrConflict
	}
	for {
		o, err := revalidate(root, current, p, &j, e)
		if err != nil {
			return Receipt{}, err
		}
		switch j.Phase {
		case "prepared":
			if current != p.SourcePlan.Root {
				_ = o.db.Close()
				return Receipt{}, managed.ErrConflict
			}
			err = createBackup(root, current, &j, o, e)
			if err != nil {
				err = fail("backup_creation", err)
			}
		case "backup_verified":
			if err = verifyBackup(p, j); err == nil {
				j.Phase = "rename_pending"
				err = updateJournal(root, current, &j, e)
			}
		case "rename_pending":
			if err = verifyBackup(p, j); err == nil {
				if current == p.SourcePlan.Root {
					err = renameRoot(root, p, current, p.TargetPlan.Root, e)
					if err == nil {
						current = p.TargetPlan.Root
					}
				} else {
					err = reconcileRename(root, p, p.SourcePlan.Root, p.TargetPlan.Root, e)
				}
				if err == nil {
					j.Phase = "moved"
					err = updateJournal(root, current, &j, e)
				}
			}
		case "moved":
			if current != p.TargetPlan.Root {
				err = managed.ErrConflict
			} else {
				j.Phase = "owner_pending"
				err = updateJournal(root, current, &j, e)
			}
		case "owner_pending":
			if err = o.db.Close(); err != nil {
				return Receipt{}, err
			}
			if current != p.TargetPlan.Root {
				return Receipt{}, managed.ErrConflict
			}
			if err = replaceOwner(root, current, j, j.TargetOwner, e); err != nil {
				return Receipt{}, err
			}
			e.boundary("owner_updated")
			// Owner publication is an effect, not proof of completion. Keep the
			// durable journal nonterminal until the final root, retained inventory
			// and logical ledger have been verified under the original locks.
			verified, err := revalidate(root, current, p, &j, e)
			if err != nil {
				return Receipt{}, err
			}
			if err = verified.db.Close(); err != nil {
				return Receipt{}, err
			}
			if err = verifyBackup(p, j); err != nil {
				return Receipt{}, err
			}
			j.Phase = "complete"
			r := receipt(j, current, backupState(p, j))
			r.Sequence++
			j.CompletionReceipt = &r
			if err = updateJournal(root, current, &j, e); err != nil {
				return Receipt{}, err
			}
			return *j.CompletionReceipt, nil
		case "complete":
			if current != p.TargetPlan.Root || j.CompletionReceipt == nil {
				err = managed.ErrConflict
			} else {
				err = verifyBackup(p, j)
			}
			end := o.db.Close()
			if err != nil {
				return Receipt{}, err
			}
			if end != nil {
				return Receipt{}, end
			}
			return *j.CompletionReceipt, nil
		default:
			err = managed.ErrConflict
		}
		end := o.db.Close()
		if err != nil {
			return Receipt{}, err
		}
		if end != nil {
			return Receipt{}, end
		}
	}
}
func rollback(root *held, current string, j *Journal, e Environment) (Receipt, error) {
	p := j.Plan
	o, err := revalidate(root, current, p, j, e)
	if err != nil {
		return Receipt{}, err
	}
	if err = o.db.Close(); err != nil {
		return Receipt{}, err
	}
	if j.Phase == "rolled_back" {
		if current != p.SourcePlan.Root || j.RollbackReceipt == nil {
			return Receipt{}, managed.ErrConflict
		}
		return *j.RollbackReceipt, nil
	}
	if j.BackupIdentity != nil {
		backup, err := backupDirectory(p, *j)
		if err != nil {
			return Receipt{}, err
		}
		backup.close()
		if j.BackupState == "verified" {
			if err = verifyBackup(p, *j); err != nil {
				return Receipt{}, err
			}
		}
	} else if current != p.SourcePlan.Root {
		return Receipt{}, managed.ErrConflict
	}
	if j.Phase != "rollback_pending" {
		j.Phase = "rollback_pending"
		if err = updateJournal(root, current, j, e); err != nil {
			return Receipt{}, err
		}
	}
	if err = replaceOwner(root, current, *j, j.OriginalOwner, e); err != nil {
		return Receipt{}, err
	}
	e.boundary("rollback_owner")
	if current == p.TargetPlan.Root {
		if err = renameRoot(root, p, current, p.SourcePlan.Root, e); err != nil {
			return Receipt{}, err
		}
		current = p.SourcePlan.Root
		e.boundary("rollback_rename")
	} else {
		if err = reconcileRename(root, p, p.TargetPlan.Root, p.SourcePlan.Root, e); err != nil {
			return Receipt{}, err
		}
	}
	if current != p.SourcePlan.Root {
		return Receipt{}, managed.ErrConflict
	}
	files, err := inventory(root, current)
	if err != nil {
		return Receipt{}, err
	}
	if err = inventoryMatches(root, current, p, j, files); err != nil {
		return Receipt{}, err
	}
	j.Phase = "rolled_back"
	r := receipt(*j, current, backupState(p, *j))
	r.Sequence++
	j.RollbackReceipt = &r
	if err = updateJournal(root, current, j, e); err != nil {
		return Receipt{}, err
	}
	return *j.RollbackReceipt, nil
}
