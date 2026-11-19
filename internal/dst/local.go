package dst

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
)

// Local is dst class local: archives in a directory, which may be on a file
// system mounted for the backup.
type Local struct {
	Common `yaml:",inline"`
	Dir    string `yaml:"dir"`
	// MountSpec is the mount section, nil without one.
	MountSpec *Mount `yaml:"mount"`
}

// Mount is the mount section of dst local.
type Mount struct {
	Point string `yaml:"point"`
	// Cmd mounts Point; without it, mount <point> does, by /etc/fstab.
	Cmd config.Command `yaml:"cmd"`
	// Umount unmounts Point after each attempt.
	Umount bool `yaml:"umount"`
}

// NewLocal builds a Local from its config section.
func NewLocal(n *yaml.Node) (*Local, error) {
	var l Local
	if err := config.Decode(n, &l); err != nil {
		return nil, err
	}
	if l.Dir == "" {
		return nil, fmt.Errorf("line %d: dir is required", n.Line)
	}
	if err := l.check(); err != nil {
		return nil, fmt.Errorf("line %d: %w", n.Line, err)
	}
	if l.MountSpec != nil && l.MountSpec.Point == "" {
		// Raku skips a mount section without keys only.
		if _, m := keyValue(n, "mount"); m != nil && len(m.Content) > 0 {
			return nil, fmt.Errorf("line %d: mount.point is required", m.Line)
		}
		l.MountSpec = nil
	}
	return &l, nil
}

// Mount implements config.Dest. When a mount point is configured and not
// mounted, it runs mount.cmd, or mount <point>, and checks that the point is
// mounted then. Either way dir must be a directory.
func (l *Local) Mount(ctx context.Context) error {
	if m := l.MountSpec; m != nil {
		mounted, err := isMountpoint(ctx, m.Point)
		if err != nil {
			return err
		}
		if !mounted {
			argv := []string(m.Cmd)
			if len(argv) == 0 {
				argv = []string{"mount", m.Point}
			}
			if err := runTool(ctx, argv); err != nil {
				return fmt.Errorf("mount %s: %w", m.Point, err)
			}
			if mounted, err = isMountpoint(ctx, m.Point); err != nil {
				return err
			}
			if !mounted {
				return fmt.Errorf("mount %s: not mounted after the mount command", m.Point)
			}
		}
	}
	fi, err := os.Stat(l.Dir)
	if err != nil {
		return fmt.Errorf("dir: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("dir %s is not a directory", l.Dir)
	}
	return nil
}

// Umount implements config.Dest: with mount.umount, it unmounts the mount
// point if it is mounted.
func (l *Local) Umount(ctx context.Context) error {
	m := l.MountSpec
	if m == nil || !m.Umount {
		return nil
	}
	mounted, err := isMountpoint(ctx, m.Point)
	if err != nil || !mounted {
		return err
	}
	if err := runTool(ctx, []string{"umount", m.Point}); err != nil {
		return fmt.Errorf("umount %s: %w", m.Point, err)
	}
	return nil
}

// isMountpoint reports whether mountpoint -q says path is a mount point.
// Any non-zero status counts as not mounted, as in Raku.
func isMountpoint(ctx context.Context, path string) (bool, error) {
	err := exec.CommandContext(ctx, "mountpoint", "-q", path).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.Exited():
		return false, nil
	}
	return false, fmt.Errorf("mountpoint %s: %w", path, err)
}

// List implements config.Dest.
func (l *Local) List(_ context.Context) ([]config.Entry, error) {
	des, err := os.ReadDir(l.Dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", l.Dir, err)
	}
	out := make([]config.Entry, 0, len(des))
	for _, de := range des {
		// The size of what a link points to, as Raku's .s gives.
		fi, err := os.Stat(filepath.Join(l.Dir, de.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue // deleted since ReadDir, or a dangling link
		}
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", l.Dir, err)
		}
		out = append(out, config.Entry{Name: de.Name(), Size: fi.Size()})
	}
	return out, nil
}

// Delete implements config.Dest. A file that is not there counts as
// deleted.
func (l *Local) Delete(_ context.Context, name string) error {
	if err := checkBase(name); err != nil {
		return err
	}
	// A file gone already is deleted, as with Raku's unlink.
	if err := os.Remove(filepath.Join(l.Dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

// Open implements config.Dest.
func (l *Local) Open(_ context.Context, name string) (io.ReadCloser, error) {
	if err := checkBase(name); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(l.Dir, name)) //nolint:gosec // G304: the path is dir and a checked base name
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return f, nil
}

// Write implements config.Dest. The archive is written to .<name>.partial,
// created 0600, and renamed to name once send succeeded and the data is
// synced to disk. On a failure the temporary file is deleted. A failure to
// sync the directory after the rename leaves the archive in place and wraps
// config.ErrInPlace.
func (l *Local) Write(_ context.Context, name string, send func(out *os.File) error) (err error) {
	if err := checkBase(name); err != nil {
		return err
	}
	final := filepath.Join(l.Dir, name)
	tmp := filepath.Join(l.Dir, "."+name+".partial")
	// One left by a run that was killed may belong to someone else or have
	// other permissions: start afresh.
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale %s: %w", tmp, err)
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: the path is dir and a checked base name
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
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
	// The umask may not take permissions away from 0600 beyond the owner's.
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := send(f); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	closed = true
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	if err := syncDir(l.Dir); err != nil {
		return fmt.Errorf("%w, but %w", config.ErrInPlace, err)
	}
	return nil
}

// syncDir makes a rename in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: the destination directory of the config
	if err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	defer d.Close() //nolint:errcheck // opened read-only for the sync only
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}
