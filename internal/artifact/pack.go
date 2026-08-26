// Package artifact packs and unpacks task artifacts: the directory trees that
// have to travel between nodes when one node produces work another node
// consumes. It is the data plane the orchestrator was missing — until now a
// delegated task carried only a *path*, which is meaningless on the machine
// that receives it.
//
// Two properties matter more than convenience here:
//
//   - Determinism. The same tree must pack to the same bytes, and therefore the
//     same hash, on every node and every run. The hash is the artifact's name in
//     storage and the integrity check after transfer, so a timestamp or a uid
//     leaking into the archive would make two identical trees look different and
//     defeat both.
//   - Distrust of the archive. An artifact arrives over the network from another
//     node. Unpack therefore treats every entry as hostile: nothing may be
//     written outside the destination, and no entry type that could reach
//     outside it (symlink to an absolute path, hard link, device node) is
//     honoured.
//
// The package depends on nothing else in OpenPanda: Pack and Unpack do no IO
// beyond the reader or writer they are given plus the destination tree, and
// Store adds only a flat, content-addressed directory on top of them.
package artifact

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// MaxBytes caps both the compressed archive and the total decompressed payload.
// An artifact is a build output or a trained model, not a disk image; the cap is
// what stands between a peer and a zip bomb that fills this node's disk.
const MaxBytes int64 = 512 << 20 // 512 MiB

// maxEntries caps the entry count so an archive of a million empty files cannot
// exhaust memory through the manifest alone.
const maxEntries = 200_000

// ErrTooLarge is returned when an archive or its decompressed content exceeds
// MaxBytes, or when it holds more than maxEntries entries.
var ErrTooLarge = errors.New("artifact: exceeds size limit")

// EntryMeta describes one file in an artifact. Paths are slash-separated and
// relative to the artifact root, so a manifest reads the same on every OS.
type EntryMeta struct {
	Path string      `json:"path"`
	Mode os.FileMode `json:"mode"`
	Size int64       `json:"size"`
}

// Manifest is what an artifact is, independently of where its bytes live: the
// hash that names it, its packed size, and the files it contains. Hash is the
// hex SHA-256 of the gzip stream — the same value on both sides of a transfer.
type Manifest struct {
	Hash    string      `json:"hash"`
	Size    int64       `json:"size"`
	Entries []EntryMeta `json:"entries"`
}

// epoch is the fixed modification time stamped on every entry. Real mtimes are
// the single largest source of hash instability: copying a tree changes them,
// so two byte-identical trees would otherwise pack to different archives.
var epoch = time.Unix(0, 0).UTC()

// Pack writes root as a deterministic tar.gz to w and returns the manifest,
// whose Hash is the SHA-256 of the bytes written. Only regular files and
// directories are packed; symlinks and every other irregular entry are skipped
// rather than followed, since following one would silently pull in data from
// outside root.
//
// Determinism comes from four choices, all of them load-bearing: entries are
// emitted in sorted path order (directory iteration order is not stable),
// ModTime is the epoch, ownership fields are cleared, and the gzip level is
// pinned. Change any of them and previously stored artifacts stop matching
// their hashes.
func Pack(root string, w io.Writer) (Manifest, error) {
	entries, err := walk(root)
	if err != nil {
		return Manifest{}, err
	}

	// Hash what we write, as we write it: one pass, and the hash covers exactly
	// the bytes the receiver will verify.
	h := sha256.New()
	counter := &countingWriter{w: io.MultiWriter(w, h)}

	gz, err := gzip.NewWriterLevel(counter, gzip.BestSpeed)
	if err != nil {
		return Manifest{}, fmt.Errorf("artifact: gzip writer: %w", err)
	}
	tw := tar.NewWriter(gz)

	meta := make([]EntryMeta, 0, len(entries))
	for _, e := range entries {
		if err := writeEntry(tw, root, e); err != nil {
			return Manifest{}, err
		}
		if !e.dir {
			meta = append(meta, EntryMeta{Path: e.rel, Mode: e.mode, Size: e.size})
		}
	}
	if err := tw.Close(); err != nil {
		return Manifest{}, fmt.Errorf("artifact: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return Manifest{}, fmt.Errorf("artifact: close gzip: %w", err)
	}
	if counter.n > MaxBytes {
		return Manifest{}, fmt.Errorf("%w: packed %d bytes", ErrTooLarge, counter.n)
	}
	return Manifest{
		Hash:    hex.EncodeToString(h.Sum(nil)),
		Size:    counter.n,
		Entries: meta,
	}, nil
}

// entry is one item to pack, carrying its root-relative slash path.
type entry struct {
	rel  string
	abs  string
	mode os.FileMode
	size int64
	dir  bool
}

// walk collects root's regular files and directories in sorted path order.
// Irregular entries (symlinks, sockets, devices) are skipped: an artifact is
// content, and a link is a reference to content that may not exist on the node
// the artifact travels to.
func walk(root string) ([]entry, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("artifact: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("artifact: root %q is not a directory", root)
	}

	var out []entry
	var total int64
	err = filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil // the root itself is implicit
		}
		rel = filepath.ToSlash(rel)
		switch {
		case fi.IsDir():
			out = append(out, entry{rel: rel, abs: path, mode: fi.Mode().Perm(), dir: true})
		case fi.Mode().IsRegular():
			total += fi.Size()
			if total > MaxBytes {
				return fmt.Errorf("%w: %s exceeds %d bytes of content", ErrTooLarge, root, MaxBytes)
			}
			out = append(out, entry{rel: rel, abs: path, mode: fi.Mode().Perm(), size: fi.Size()})
		default:
			// Symlink, socket, device, FIFO: not content, not packed.
		}
		if len(out) > maxEntries {
			return fmt.Errorf("%w: more than %d entries", ErrTooLarge, maxEntries)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// writeEntry emits one tar entry with every non-content field normalized, so
// the archive depends on the tree's content and layout and nothing else.
func writeEntry(tw *tar.Writer, root string, e entry) error {
	hdr := &tar.Header{
		Name:     e.rel,
		Mode:     int64(e.mode.Perm()),
		ModTime:  epoch,
		Format:   tar.FormatPAX,
		Typeflag: tar.TypeReg,
		Size:     e.size,
	}
	if e.dir {
		hdr.Name = e.rel + "/"
		hdr.Typeflag = tar.TypeDir
		hdr.Size = 0
	}
	// Ownership is host state, never artifact content: an identical tree packed
	// by two different users must hash identically.
	hdr.Uid, hdr.Gid = 0, 0
	hdr.Uname, hdr.Gname = "", ""
	hdr.AccessTime, hdr.ChangeTime = time.Time{}, time.Time{}

	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("artifact: write header %s: %w", e.rel, err)
	}
	if e.dir {
		return nil
	}
	f, err := os.Open(e.abs)
	if err != nil {
		return fmt.Errorf("artifact: open %s: %w", e.rel, err)
	}
	defer f.Close()
	// Copy exactly the size declared in the header: a file that grew between
	// the walk and now would otherwise corrupt the stream.
	n, err := io.Copy(tw, io.LimitReader(f, e.size))
	if err != nil {
		return fmt.Errorf("artifact: copy %s: %w", e.rel, err)
	}
	if n != e.size {
		return fmt.Errorf("artifact: %s changed while packing (%d of %d bytes)", e.rel, n, e.size)
	}
	return nil
}

// countingWriter counts bytes written so Pack can report the packed size (and
// enforce the cap) without buffering the archive.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
