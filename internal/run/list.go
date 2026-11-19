package run

import (
	"context"
	"fmt"
	"io"

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
