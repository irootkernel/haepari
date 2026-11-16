package relocation

import (
	"os"

	"github.com/irootkernel/haepari/internal/managed"
)

// Activate runs under the controller's original kernel ownership, before the
// ordinary SQLite opener, migrations, config commits or stale-socket repair.
// Later legitimate work is allowed: activated startup checks installation/root
// authority, not the pre-move logical inventory used only for rollback.
func Activate(rootPath string, controller *os.File) error {
	return activate(rootPath, controller, Environment{})
}

func activate(rootPath string, controller *os.File, e Environment) error {
	if controller == nil {
		return fail("startup_lock", managed.ErrInvalid)
	}
	root, err := openDirectory(rootPath)
	if err != nil {
		return fail("startup_root", err)
	}
	defer root.close()
	j, _, _, err := readJournal(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fail("startup_journal", err)
	}
	if !terminal(j) {
		return fail("startup_partial", managed.ErrBlocked)
	}
	current := j.Plan.TargetPlan
	if j.Phase == "rolled_back" {
		current = j.Plan.SourcePlan
	}
	if current.Root != rootPath || root.record != j.Plan.SourceRootIdentity {
		return fail("startup_identity", managed.ErrConflict)
	}
	if err = bindOriginalController(root, rootPath, j, controller); err != nil {
		return fail("startup_lock", err)
	}
	if _, err = ownerBytes(root, current); err != nil {
		return fail("startup_owner", err)
	}
	if err = verifyHistory(root, j); err != nil {
		return fail("startup_history", err)
	}
	if err = installationIdentity(root, j.Plan); err != nil {
		return fail("startup_installation", err)
	}
	if err = syncDirectory(root, rootPath, e); err != nil {
		return fail("startup_journal_sync", err)
	}
	if j.Activated {
		return nil
	}
	objects, err := openAt(root.fd(), "objects", true)
	if err != nil {
		return fail("startup_store_lock", err)
	}
	defer objects.close()
	store, err := lockExisting(objects, ".seal.lock")
	if err != nil {
		return fail("startup_store_lock", err)
	}
	defer store.close()
	j.Activated = true
	if err = updateJournal(root, rootPath, &j, e); err != nil {
		return fail("startup_activation", err)
	}
	return nil
}
