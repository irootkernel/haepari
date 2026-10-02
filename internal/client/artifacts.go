package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/irootkernel/haepari/internal/managed"
)

func (c *Client) Artifacts(ctx context.Context, id string) (managed.ArtifactManifest, Envelope, error) {
	var manifest managed.ArtifactManifest
	e, err := c.call(ctx, "GET", "/v1/jobs/"+url.PathEscape(id)+"/artifacts", nil, &manifest)
	return manifest, e, err
}

func (c *Client) Purge(ctx context.Context, id string, revision int64) (managed.PurgeReceipt, Envelope, error) {
	var receipt managed.PurgeReceipt
	e, err := c.call(ctx, "POST", "/v1/jobs/"+url.PathEscape(id)+"/purge", map[string]any{"expected_revision": revision}, &receipt)
	return receipt, e, err
}

func (c *Client) ArtifactBytes(ctx context.Context, id string, file managed.DeliveryFile, after int64) (managed.ArtifactPage, Envelope, error) {
	var page managed.ArtifactPage
	q := url.Values{"path": {file.Path}, "sha256": {file.SHA256}, "size": {strconv.FormatInt(file.SizeBytes, 10)}, "page_token": {file.PageToken}, "after": {strconv.FormatInt(after, 10)}}
	e, err := c.call(ctx, "GET", "/v1/jobs/"+url.PathEscape(id)+"/artifacts?"+q.Encode(), nil, &page)
	return page, e, err
}

// Export creates an exclusive destination. A failed export retains its partial
// directory for inspection; it never removes or overwrites unrelated content.
func (c *Client) Export(ctx context.Context, id, destination string) (managed.ArtifactManifest, Envelope, error) {
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination || destination == "/" {
		return managed.ArtifactManifest{}, Envelope{}, managed.ErrInvalid
	}
	m, e, err := c.Artifacts(ctx, id)
	if err != nil {
		return m, e, err
	}
	if m.Schema != "haepari.artifact-manifest/v1" || m.JobID != id || m.Result.JobID != id || len(m.Files) > 3001 {
		return m, Envelope{}, managed.ErrInvalid
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if !managed.ValidArtifactPath(f.Path) || f.Path != "output.log" && !strings.HasPrefix(f.Path, "artifacts/") && !strings.HasPrefix(f.Path, "recovery-") || seen[f.Path] || f.SizeBytes < 0 || f.SizeBytes > 1<<30 || f.PageToken == "" {
			return m, Envelope{}, managed.ErrInvalid
		}
		seen[f.Path] = true
	}
	parent, err := os.OpenRoot(filepath.Dir(destination))
	if err != nil {
		return m, Envelope{}, err
	}
	defer parent.Close()
	if err = parent.Mkdir(filepath.Base(destination), 0700); err != nil {
		return m, Envelope{}, err
	}
	dir, err := parent.OpenRoot(filepath.Base(destination))
	if err != nil {
		return m, Envelope{}, err
	}
	defer dir.Close()
	writeJSON := func(name string, v any) error {
		f, err := dir.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		if err = json.NewEncoder(f).Encode(v); err != nil {
			return err
		}
		return f.Sync()
	}
	if err = writeJSON("result.json", m.Result); err != nil {
		return m, Envelope{}, err
	}
	for _, file := range m.Files {
		if err = dir.MkdirAll(filepath.Dir(file.Path), 0700); err != nil {
			return m, Envelope{}, err
		}
		f, err := dir.OpenFile(file.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return m, Envelope{}, err
		}
		h := sha256.New()
		offset := int64(0)
		for {
			p, pageEnvelope, pageErr := c.ArtifactBytes(ctx, id, file, offset)
			if pageErr != nil {
				f.Close()
				return m, pageEnvelope, pageErr
			}
			if p.Schema != "haepari.artifact-page/v1" || p.JobID != id || p.File != file.ResultArtifact || p.Offset != offset || len(p.Data) > 64<<10 || p.NextOffset != offset+int64(len(p.Data)) || p.NextOffset > file.SizeBytes || p.More != (p.NextOffset < file.SizeBytes) || p.More && len(p.Data) == 0 {
				f.Close()
				return m, Envelope{}, managed.ErrInvalid
			}
			n, writeErr := f.Write(p.Data)
			if writeErr != nil || n != len(p.Data) {
				f.Close()
				return m, Envelope{}, fmt.Errorf("export write failed: %w", writeErr)
			}
			h.Write(p.Data)
			offset = p.NextOffset
			if !p.More {
				break
			}
		}
		if offset != file.SizeBytes || hex.EncodeToString(h.Sum(nil)) != file.SHA256 {
			f.Close()
			return m, Envelope{}, fmt.Errorf("export checksum mismatch for %s", file.Path)
		}
		if err = f.Sync(); err != nil {
			f.Close()
			return m, Envelope{}, err
		}
		if err = f.Close(); err != nil {
			return m, Envelope{}, err
		}
	}
	// The final manifest is the completion receipt. Tokens are transient server
	// handles and are not exported into a durable/shareable inventory.
	for i := range m.Files {
		m.Files[i].PageToken = ""
	}
	if err = writeJSON("manifest.json", m); err != nil {
		return m, Envelope{}, err
	}
	return m, e, nil
}
