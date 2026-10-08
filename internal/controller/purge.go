package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/irootkernel/haepari/internal/managed"
	"github.com/irootkernel/haepari/internal/snapshot"
	"golang.org/x/sys/unix"
)

// Inventory the exact leaves and directory incarnations before recording a
// deletion intent. Unknown files and links are refused, even inside owned roots.
func planBulkPaths(ctx context.Context, parent int, name string, allowed map[string]bool, paths *[]managed.RetainedPath) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(parent, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	directory := stat.Mode&unix.S_IFMT == unix.S_IFDIR
	if directory {
		known := name == "artifacts" || allowed[name+"/"]
		for p := range allowed {
			known = known || strings.HasPrefix(p, name+"/")
		}
		if !known {
			return managed.ErrBlocked
		}
	}
	if !directory && !allowed[name] {
		return managed.ErrBlocked
	}
	if !directory && stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 || !directory && stat.Nlink != 1 {
		return managed.ErrBlocked
	}
	if len(*paths) >= 16000 {
		return managed.ErrBlocked
	}
	if directory {
		fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), name)
		defer f.Close()
		entries, err := f.ReadDir(16001)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(entries) > 16000 {
			return managed.ErrBlocked
		}
		for _, entry := range entries {
			childAllowed := map[string]bool{}
			for p := range allowed {
				if strings.HasPrefix(p, name+"/") {
					childAllowed[strings.TrimPrefix(p, name+"/")] = true
				}
			}
			var children []managed.RetainedPath
			if err = planBulkPaths(ctx, fd, entry.Name(), childAllowed, &children); err != nil {
				return err
			}
			for _, p := range children {
				if len(*paths) >= 16000 {
					return managed.ErrBlocked
				}
				p.Path = name + "/" + p.Path
				*paths = append(*paths, p)
			}
		}
	}
	*paths = append(*paths, managed.RetainedPath{Path: name, Device: uint64(stat.Dev), Inode: stat.Ino, Directory: directory})
	return nil
}

func removeRetainedPath(root int, p managed.RetainedPath, fences map[string]managed.RetainedPath) error {
	parts := strings.Split(p.Path, "/")
	fd, err := unix.Dup(root)
	if err != nil {
		return err
	}
	defer func() { unix.Close(fd) }()
	prefix := ""
	for _, part := range parts[:len(parts)-1] {
		if prefix != "" {
			prefix += "/"
		}
		prefix += part
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err != nil {
			return err
		}
		var st unix.Stat_t
		fence, ok := fences[prefix]
		if err = unix.Fstat(next, &st); err != nil || !ok || uint64(st.Dev) != fence.Device || st.Ino != fence.Inode || !fence.Directory {
			unix.Close(next)
			return managed.ErrBlocked
		}
		unix.Close(fd)
		fd = next
	}
	name := parts[len(parts)-1]
	var st unix.Stat_t
	if err = unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return err
	}
	if uint64(st.Dev) != p.Device || st.Ino != p.Inode || p.Directory != (st.Mode&unix.S_IFMT == unix.S_IFDIR) {
		return managed.ErrBlocked
	}
	flags := 0
	if p.Directory {
		flags = unix.AT_REMOVEDIR
	}
	return unix.Unlinkat(fd, name, flags)
}

func (s *runtimeServer) purgeJob(id string, revision int64) (managed.PurgeReceipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	p, err := s.purgeJobContext(ctx, id, revision)
	if err != nil && ctx.Err() != nil {
		return managed.PurgeReceipt{}, managed.ErrBlocked
	}
	return p, err
}

func (s *runtimeServer) purgeJobContext(ctx context.Context, id string, revision int64) (managed.PurgeReceipt, error) {
	for {
		if err := ctx.Err(); err != nil {
			return managed.PurgeReceipt{}, err
		}
		if s.evidenceMu.TryLock() {
			break
		}
		select {
		case <-ctx.Done():
			return managed.PurgeReceipt{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer s.evidenceMu.Unlock()
	q, found, complete, err := s.ledger.PurgeIntent(id)
	if err != nil {
		return managed.PurgeReceipt{}, err
	}
	if found {
		if q.ExpectedRevision != revision {
			return managed.PurgeReceipt{}, managed.ErrStale
		}
		if complete {
			return s.ledger.CompletePurge(q)
		}
	} else {
		b, err := s.ledger.BulkSnapshot(id)
		if err != nil {
			return managed.PurgeReceipt{}, err
		}
		if b.Receipt.JobRevision != revision {
			return managed.PurgeReceipt{}, managed.ErrStale
		}
		if s.jobWorkerAlive(id) {
			return managed.PurgeReceipt{}, managed.ErrBlocked
		}
		sealed, err := s.store.ReadContext(ctx, b.Receipt.Admission.ObjectID)
		if err != nil {
			return managed.PurgeReceipt{}, err
		}
		if len(b.Inventory) == 0 {
			inventory, err := json.Marshal(sealed.Manifest)
			if err != nil {
				return managed.PurgeReceipt{}, err
			}
			if err = s.ledger.BindAdmissionInventory(b.Receipt.Admission.RequestKey, b.Receipt.Admission.ObjectID, inventory); err != nil {
				return managed.PurgeReceipt{}, err
			}
			b, err = s.ledger.BulkSnapshot(id)
			if err != nil {
				return managed.PurgeReceipt{}, err
			}
		}
		_, err = snapshot.DecodeInventory(b.Inventory, b.Receipt.Admission.ObjectID)
		if err != nil {
			return managed.PurgeReceipt{}, err
		}
		dev, ino, err := s.store.ObjectIdentity(b.Receipt.Admission.ObjectID)
		if err != nil {
			return managed.PurgeReceipt{}, err
		}
		q = managed.PurgeIntent{Schema: "haepari.purge-intent/v1", JobID: id, ExpectedRevision: revision, SnapshotDigest: managed.RecoveryDigest(b), Paths: []managed.RetainedPath{}, RecordedAt: time.Now().UTC(), Object: &managed.RetainedPath{Path: b.Receipt.Admission.ObjectID, Device: dev, Inode: ino, Directory: true}}
		objectPaths, err := s.store.PlanPurgeOwned(ctx, q.Object.Path, dev, ino, b.Inventory)
		if err != nil {
			return managed.PurgeReceipt{}, err
		}
		for _, p := range objectPaths {
			q.ObjectPaths = append(q.ObjectPaths, managed.RetainedPath{Path: p.Path, Device: p.Device, Inode: p.Inode, Directory: p.Directory})
		}
		fd, err := openPrivateJob(s.root.Path, id)
		if err == nil {
			defer unix.Close(fd)
			if b.Identity == nil {
				return managed.PurgeReceipt{}, managed.ErrBlocked
			}
			owned, err := s.inspectRecoveryRoot(*b.Identity)
			if err != nil || !owned {
				return managed.PurgeReceipt{}, managed.ErrBlocked
			}
			q.Root, err = retainedPath(fd, "", true)
			if err != nil {
				return managed.PurgeReceipt{}, err
			}
			var workspace unix.Stat_t
			if err = unix.Fstatat(fd, "workspace", &workspace, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
				return managed.PurgeReceipt{}, managed.ErrBlocked
			}
			allowed := map[string]bool{"output.log": true}
			allowParents := func(root string) {
				for _, output := range b.Spec.Workload.RequiredOutputs {
					parts := strings.Split(output, "/")
					for i := 1; i < len(parts); i++ {
						allowed[root+"/"+strings.Join(parts[:i], "/")+"/"] = true
					}
				}
			}
			allowParents("artifacts")
			for _, a := range b.Result.Artifacts {
				allowed["artifacts/"+a.Path] = true
			}
			names := []string{"artifacts", "output.log"}
			records, err := s.ledger.SupplementaryEvidence(id)
			if err != nil {
				return managed.PurgeReceipt{}, err
			}
			seen := map[string]bool{}
			for _, record := range records {
				if record.ArtifactRoot == "" {
					continue
				}
				allowed[record.ArtifactRoot+"/"] = true
				allowParents(record.ArtifactRoot)
				if !seen[record.ArtifactRoot] {
					names = append(names, record.ArtifactRoot)
					seen[record.ArtifactRoot] = true
				}
				for _, a := range record.Artifacts {
					allowed[record.ArtifactRoot+"/"+a.Path] = true
				}
				if len(record.GuestArtifacts) > 0 {
					nativeRoot := record.ArtifactRoot + "-native"
					allowed[nativeRoot+"/"] = true
					if !seen[nativeRoot] {
						names = append(names, nativeRoot)
						seen[nativeRoot] = true
					}
					for _, a := range record.GuestArtifacts {
						allowed[nativeRoot+"/"+a.Path] = true
					}
				}
			}
			for _, name := range names {
				if err = planBulkPaths(ctx, fd, name, allowed, &q.Paths); err != nil {
					return managed.PurgeReceipt{}, err
				}
			}
		} else if b.Identity != nil || !errors.Is(err, errLogMissing) {
			if errors.Is(err, errLogMissing) {
				err = managed.ErrBlocked
			}
			return managed.PurgeReceipt{}, err
		}
		if err = ctx.Err(); err != nil {
			return managed.PurgeReceipt{}, err
		}
		if err = s.ledger.BeginPurge(q); err != nil {
			return managed.PurgeReceipt{}, err
		}
		observeRetentionBoundary("purge-intent")
	}
	if err := ctx.Err(); err != nil {
		return managed.PurgeReceipt{}, err
	}
	if len(q.Paths) > 0 {
		fd, err := openPrivateJob(s.root.Path, id)
		if err != nil {
			return managed.PurgeReceipt{}, err
		}
		defer unix.Close(fd)
		root, err := retainedPath(fd, "", true)
		if err != nil || root != q.Root {
			return managed.PurgeReceipt{}, managed.ErrBlocked
		}
		fences := map[string]managed.RetainedPath{}
		for _, p := range q.Paths {
			fences[p.Path] = p
		}
		for _, p := range q.Paths {
			if err := ctx.Err(); err != nil {
				return managed.PurgeReceipt{}, err
			}
			if err = removeRetainedPath(fd, p, fences); err != nil {
				return managed.PurgeReceipt{}, err
			}
			observeRetentionBoundary("purge-leaf-removed")
		}
		if err = unix.Fsync(fd); err != nil {
			return managed.PurgeReceipt{}, err
		}
	}
	b, err := s.ledger.BulkSnapshot(id)
	if err != nil {
		return managed.PurgeReceipt{}, err
	}
	if q.Object != nil {
		count, err := s.ledger.AvailableObjectReferences(q.Object.Path)
		if err != nil {
			return managed.PurgeReceipt{}, err
		}
		if count == 0 {
			paths := make([]snapshot.PurgePath, len(q.ObjectPaths))
			for i, p := range q.ObjectPaths {
				paths[i] = snapshot.PurgePath{Path: p.Path, Device: p.Device, Inode: p.Inode, Directory: p.Directory}
			}
			err = s.store.PurgeOwned(ctx, q.Object.Path, q.Object.Device, q.Object.Inode, b.Inventory, paths)
			if err != nil {
				return managed.PurgeReceipt{}, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return managed.PurgeReceipt{}, err
	}
	return s.ledger.CompletePurge(q)
}

func (s *runtimeServer) purge(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if err := decodeBody(r, &q); err != nil {
		reject(w, "purge", err)
		return
	}
	if q.ExpectedRevision <= 0 {
		reject(w, "purge", managed.ErrInvalid)
		return
	}
	type answer struct {
		receipt managed.PurgeReceipt
		err     error
	}
	done := make(chan answer, 1)
	if !s.startWorker(func() { p, e := s.purgeJob(r.PathValue("id"), q.ExpectedRevision); done <- answer{p, e} }) {
		reject(w, "purge", managed.ErrBlocked)
		return
	}
	var result answer
	select {
	case result = <-done:
	case <-r.Context().Done():
		return
	}
	p, err := result.receipt, result.err
	if err != nil {
		reject(w, "purge", err)
		return
	}
	send(w, 200, "purge", p, nil)
}
