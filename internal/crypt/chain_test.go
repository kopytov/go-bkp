package crypt

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kopytov/go-bkp/internal/compress"
	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/shape"
	"github.com/kopytov/go-bkp/internal/src"
)

// part is a section of a profile: a class and its attributes in YAML. The
// zero part is a section left out.
type part struct{ class, text string }

// TestChain builds profiles from the real classes of every namespace and
// checks their suffixes and the order of their stages.
func TestChain(t *testing.T) {
	t.Parallel()
	dump := filepath.Join(t.TempDir(), "dump.rdb")
	if err := os.WriteFile(dump, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	filter := func(t *testing.T, table config.Table[config.Filter], p part) config.Filter {
		t.Helper()
		if p.class == "" {
			return nil
		}
		f, err := table[p.class](section(t, p.text))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	ssl := part{"openssl", "key: " + key}
	tests := []struct {
		name                   string
		src                    part
		compress, crypt, shape part
		suffix                 string
		stages                 []string
	}{
		{
			name: "tar gzip openssl pv", src: part{"tar", "files: a"},
			compress: part{"gzip", "{}"}, crypt: ssl, shape: part{"pv", "{}"},
			suffix: "tar.gz.enc", stages: []string{"src tar", "compress gzip", "crypt openssl", "shape pv"},
		},
		{
			name: "mysql gzip", src: part{"mysql", "dbs: db1"}, compress: part{"gzip", "{}"},
			suffix: "sql.gz", stages: []string{"src mysql", "compress gzip"},
		},
		{
			name: "mysql pigz openssl", src: part{"mysql", "{}"}, compress: part{"pigz", "{}"}, crypt: ssl,
			suffix: "sql.gz.enc", stages: []string{"src mysql", "compress pigz", "crypt openssl"},
		},
		{name: "redis", src: part{"redis", "dump: " + dump}, suffix: "rdb"},
		{
			name: "redis pv", src: part{"redis", "dump: " + dump}, shape: part{"pv", "rate-limit: 5m"},
			suffix: "rdb", stages: []string{"shape pv"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, err := src.Classes[tt.src.class](section(t, tt.src.text))
			if err != nil {
				t.Fatal(err)
			}
			p := config.Profile{
				Src:      s,
				Compress: filter(t, compress.Classes, tt.compress),
				Crypt:    filter(t, Classes, tt.crypt),
				Shape:    filter(t, shape.Classes, tt.shape),
			}
			if got := p.Suffix(); got != tt.suffix {
				t.Errorf("suffix %q, want %q", got, tt.suffix)
			}
			spec, err := p.Spec()
			if err != nil {
				t.Fatal(err)
			}
			defer spec.Close()
			var names []string
			for _, st := range spec.Stages {
				names = append(names, st.Name)
			}
			if !slices.Equal(names, tt.stages) {
				t.Errorf("stages %q, want %q", names, tt.stages)
			}
		})
	}
}
