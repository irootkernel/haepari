package relocation

import (
	"bytes"
	"os"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/store/sqlite"
	"golang.org/x/sys/unix"
)

// installationIdentity observes an engine-consistent copied database without
// opening the original SQLite files. Concurrent source changes refuse the read.
// Logical contents may legitimately change after activation; physical lineage
// and the persisted installation identity remain required.
func installationIdentity(root *held, p Plan) error {
	database, before, err := readRegular(root.fd(), "state.sqlite")
	if err != nil {
		return err
	}
	var original File
	for _, f := range p.Files {
		if f.Path == "state.sqlite" {
			original = f
		}
	}
	if before.Identity != original.Identity || before.Mode != original.Mode || before.OwnerUID != original.OwnerUID {
		return managed.ErrConflict
	}
	wal, walBefore, err := readRegular(root.fd(), "state.sqlite-wal")
	absentWAL := os.IsNotExist(err)
	if err != nil && !absentWAL {
		return err
	}
	if absentWAL {
		wal = nil
	}
	o, err := sqlite.Observe(database, wal)
	if err != nil {
		return err
	}
	var id string
	err = o.Tx.QueryRow("SELECT id FROM managed_installation WHERE singleton=1").Scan(&id)
	end := o.Close()
	if err != nil || end != nil || id != p.InstallationID {
		return managed.ErrConflict
	}
	afterBytes, after, err := readRegular(root.fd(), "state.sqlite")
	if err != nil || !bytes.Equal(afterBytes, database) || !bytes.Equal(canonical(before), canonical(after)) {
		return managed.ErrConflict
	}
	afterBytes, after, err = readRegular(root.fd(), "state.sqlite-wal")
	if absentWAL {
		if !os.IsNotExist(err) {
			return managed.ErrConflict
		}
	} else if err != nil || !bytes.Equal(wal, afterBytes) || !bytes.Equal(canonical(walBefore), canonical(after)) {
		return managed.ErrConflict
	}
	return nil
}

func status(p Plan, e Environment) (Receipt, error) {
	if err := validatePairShape(p); err != nil {
		return Receipt{}, err
	}
	root, current, err := locate(p)
	if err != nil {
		return Receipt{}, err
	}
	defer root.close()
	j, entry, raw, err := readJournal(root)
	if journalAbsent(err) || err == nil && predecessorMatches(p, j, entry, raw, current) {
		// A planned receipt does not invent a journal. The original unchanged-plan
		// observation still requires offline ownership and complete eligibility.
		release, err := acquire(root)
		if err != nil {
			return Receipt{}, err
		}
		defer release()
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
		return Receipt{Schema: ReceiptSchema, RelocationID: p.RelocationID, PlanSHA256: p.PlanSHA256, InstallationID: p.InstallationID, Phase: "planned", SourceRoot: p.SourcePlan.Root, TargetRoot: p.TargetPlan.Root, CurrentRoot: current, BackupState: "not_created"}, nil
	}
	if err != nil {
		return Receipt{}, err
	}
	// Inspect the actual current journal before traversing archived receipts.
	// Old binaries/parents are historical facts, never mutation authority here.
	if root.record != j.Plan.SourceRootIdentity || j.Plan.InstallationID != p.InstallationID || current != j.Plan.SourcePlan.Root && current != j.Plan.TargetPlan.Root {
		return Receipt{}, managed.ErrConflict
	}
	owner, ownerFile, err := readRegular(root.fd(), OwnerName)
	if err != nil || ownerFile.Mode&0077 != 0 || !bytes.Equal(owner, j.OriginalOwner) && !bytes.Equal(owner, j.TargetOwner) {
		return Receipt{}, managed.ErrConflict
	}
	if j.Phase == "complete" && (current != j.Plan.TargetPlan.Root || !bytes.Equal(owner, j.TargetOwner)) || j.Phase == "rolled_back" && (current != j.Plan.SourcePlan.Root || !bytes.Equal(owner, j.OriginalOwner)) {
		return Receipt{}, managed.ErrConflict
	}
	if err = installationIdentity(root, j.Plan); err != nil {
		return Receipt{}, err
	}
	if err = verifyHistory(root, j); err != nil {
		return Receipt{}, err
	}
	if j.BackupIdentity != nil {
		backup, err := backupDirectory(j.Plan, j)
		if err != nil {
			return Receipt{}, err
		}
		backup.close()
		if j.BackupState == "verified" {
			if err = verifyBackup(j.Plan, j); err != nil {
				return Receipt{}, err
			}
		}
	}
	var result Receipt
	if j.PlanSHA256 != p.PlanSHA256 {
		result, err = archivedReceipt(root, j, p)
	} else if !bytes.Equal(canonical(j.Plan), canonical(p)) {
		return Receipt{}, managed.ErrConflict
	} else if j.Phase == "complete" {
		result = *j.CompletionReceipt
	} else if j.Phase == "rolled_back" {
		result = *j.RollbackReceipt
	} else {
		result = receipt(j, current, backupState(j.Plan, j))
	}
	if err != nil {
		return Receipt{}, err
	}
	_, finalFile, finalRaw, err := readJournal(root)
	if err != nil || !bytes.Equal(raw, finalRaw) || entry.Identity != finalFile.Identity || root.matches(unix.AT_FDCWD, current) != nil {
		return Receipt{}, managed.ErrConflict
	}
	return result, nil
}
