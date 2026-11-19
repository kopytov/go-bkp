package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/kopytov/go-bkp/internal/config"
)

// ListProfiles writes the names of the profiles of the config at path to w,
// one a line, sorted.
func ListProfiles(ctx context.Context, w io.Writer, classes config.Classes, path string) error {
	profiles, err := config.Load(ctx, path, classes)
	if err != nil {
		return err
	}
	for _, p := range profiles {
		if _, err := fmt.Fprintln(w, p.Name); err != nil {
			return fmt.Errorf("write: %w", err)
		}
	}
	return nil
}

// List writes the archives of the config at path to w in the format of
// Raku's bkp list, which the metrics collector parses: <file> <size> a
// line, sorted by name, the size in bytes with bytes and as formatSize
// gives it otherwise. Only archives of the profile are listed, not other
// files of the destination. With profile set, only that profile is listed.
// Otherwise, when the config has several profiles, each group starts with
// a line <name>: and an empty line, and an empty line separates groups.
// The destination is mounted for the listing and released after it.
func List(ctx context.Context, w io.Writer, classes config.Classes, path string, bytes bool, profile string) error {
	profiles, err := config.Load(ctx, path, classes)
	if err != nil {
		return err
	}
	if profile != "" {
		i := slices.IndexFunc(profiles, func(p config.Profile) bool { return p.Name == profile })
		if i < 0 {
			return fmt.Errorf("profile %s not known in %s", profile, path)
		}
		profiles = profiles[i : i+1]
	}
	headers := len(profiles) > 1
	for i, p := range profiles {
		if headers {
			if i > 0 {
				if _, err := fmt.Fprintln(w); err != nil {
					return fmt.Errorf("write: %w", err)
				}
			}
			if _, err := fmt.Fprintf(w, "%s:\n\n", p.Name); err != nil {
				return fmt.Errorf("write: %w", err)
			}
		}
		if err := listProfile(ctx, w, p, bytes); err != nil {
			return fmt.Errorf("profile %s: %w", p.Name, err)
		}
	}
	return nil
}

// listProfile writes the archives of p, mounting its destination for that.
func listProfile(ctx context.Context, w io.Writer, p config.Profile, bytes bool) (err error) {
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
	sizes := make(map[string]int64, len(entries))
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		sizes[e.Name] = e.Size
		names = append(names, e.Name)
	}
	for _, name := range p.Dst.Naming(p.Suffix()).Archives(names) {
		size := strconv.FormatInt(sizes[name], 10)
		if !bytes {
			size = formatSize(sizes[name])
		}
		if _, err := fmt.Fprintf(w, "%s %s\n", name, size); err != nil {
			return fmt.Errorf("write: %w", err)
		}
	}
	return nil
}

// umount releases d even when ctx is done, for at most umountTimeout.
func umount(ctx context.Context, d config.Dest) error {
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), umountTimeout)
	defer cancel()
	return d.Umount(uctx)
}

// formatSize gives size as Raku's bkp list does: in the largest unit of
// 1024 bytes it reaches, with one decimal, as 1.5K, or in bytes, as 512B.
func formatSize(size int64) string {
	units := []struct {
		factor int64
		suffix string
	}{
		{1 << 40, "T"},
		{1 << 30, "G"},
		{1 << 20, "M"},
		{1 << 10, "K"},
	}
	for _, u := range units {
		if size >= u.factor {
			return fmt.Sprintf("%.1f%s", float64(size)/float64(u.factor), u.suffix)
		}
	}
	return fmt.Sprintf("%dB", size)
}
