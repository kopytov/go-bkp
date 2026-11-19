package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/kopytov/go-bkp/internal/config"
)

// Download is a download of one archive of a config.
type Download struct {
	// Config is the path of the config.
	Config string
	// Archive is the name of the archive in the destination.
	Archive string
	// Profile names the profile; it may be empty when the config has one.
	Profile string
	// Filename is where the archive goes: "-" for Stdout, empty for a file
	// named Archive in the current directory.
	Filename string
	// Force replaces an existing Filename.
	Force bool
}

// Run copies the archive of the profile out of its destination, which is
// mounted for that and released after it. Only an archive the profile lists
// is copied, not any file of the destination. A file is written under a
// temporary name next to it, created 0600, and renamed once complete; an
// existing one is replaced only with Force.
func (d Download) Run(ctx context.Context, classes config.Classes, stdout io.Writer) error {
	profiles, err := config.Load(ctx, d.Config, classes)
	if err != nil {
		return err
	}
	var p config.Profile
	switch {
	case d.Profile != "":
		i := slices.IndexFunc(profiles, func(p config.Profile) bool { return p.Name == d.Profile })
		if i < 0 {
			return fmt.Errorf("profile %s not known in %s", d.Profile, d.Config)
		}
		p = profiles[i]
	case len(profiles) > 1:
		return fmt.Errorf("--profile should be specified for %s", d.Config)
	default:
		p = profiles[0]
	}
	filename := d.Filename
	if filename == "" {
		filename = d.Archive
	}
	if filename != "-" && !d.Force {
		if err := absent(filename); err != nil {
			return err
		}
	}
	if err := d.copy(ctx, p, filename, stdout); err != nil {
		return fmt.Errorf("profile %s: %w", p.Name, err)
	}
	return nil
}

// absent returns an error when filename exists.
func absent(filename string) error {
	_, err := os.Lstat(filename)
	switch {
	case err == nil:
		return fmt.Errorf("%s exists, use --force option", filename)
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("check %s: %w", filename, err)
	}
	return nil
}

// copy copies the archive of p to filename, or to stdout for "-".
func (d Download) copy(ctx context.Context, p config.Profile, filename string, stdout io.Writer) (err error) {
	defer func() {
		if uerr := umount(ctx, p.Dst); uerr != nil {
			err = errors.Join(err, fmt.Errorf("dst: %w", uerr))
		}
	}()
	if err := p.Dst.Mount(ctx); err != nil {
		return fmt.Errorf("dst: %w", err)
	}
	entries, err := p.Dst.List(ctx)
	if err != nil {
		return fmt.Errorf("dst: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if !slices.Contains(p.Dst.Naming(p.Suffix()).Archives(names), d.Archive) {
		return fmt.Errorf("no archive %s", d.Archive)
	}
	in, err := p.Dst.Open(ctx, d.Archive)
	if err != nil {
		return fmt.Errorf("dst: %w", err)
	}
	defer func() {
		// The file is in place by now: a reader must report a broken
		// transfer from Read, as the ftp one does. A failure here still
		// fails the download.
		if cerr := in.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("dst: %w", cerr)
		}
	}()
	src := ctxReader{ctx: ctx, r: in}
	if filename == "-" {
		if _, err := io.Copy(stdout, src); err != nil {
			return fmt.Errorf("copy %s: %w", d.Archive, err)
		}
		return nil
	}
	return writeFile(filename, d.Force, src)
}

// writeFile writes r to filename through a temporary file next to it,
// created 0600 and renamed once complete. Without force, an existing
// filename is an error and stays as it is. On a failure the temporary
// file is deleted.
func writeFile(filename string, force bool, r io.Reader) (err error) {
	f, err := os.CreateTemp(filepath.Dir(filename), "."+filepath.Base(filename)+".*.partial")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", filename, err)
	}
	tmp := f.Name()
	closed := false
	defer func() {
		if err == nil {
			return
		}
		if !closed {
			f.Close() //nolint:errcheck,gosec // the write failed already, its file goes
		}
		if rmErr := os.Remove(tmp); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove %s: %w", tmp, rmErr))
		}
	}()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	closed = true
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	// Checked again, as a file may have appeared during the download.
	if !force {
		if err := absent(filename); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, filename); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}

// ctxReader stops reading r once ctx is done, so that a signal stops a
// download from a destination that does not watch ctx itself.
type ctxReader struct {
	ctx context.Context //nolint:containedctx // bounds the reads of one copy, as the reader lives only for it
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
