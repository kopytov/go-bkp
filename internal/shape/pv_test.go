package shape

import (
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func section(t *testing.T, text string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("unmarshal %q: %v", text, err)
	}
	return doc.Content[0]
}

func TestPv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, text string
		argv       []string
	}{
		{"defaults", "{}", []string{"pv", "-q", "-L", "1m"}},
		{"rate-limit", "rate-limit: 5m", []string{"pv", "-q", "-L", "5m"}},
		{"rate-limit a number", "rate-limit: 1048576", []string{"pv", "-q", "-L", "1048576"}},
		{"cmd", "cmd: [ionice, -c3, pv, -q]\nrate-limit: 10k", []string{"ionice", "-c3", "pv", "-q", "-L", "10k"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, err := Classes["pv"](section(t, tt.text))
			if err != nil {
				t.Fatal(err)
			}
			if got := f.Suffix("tar.gz"); got != "tar.gz" {
				t.Errorf("suffix %q, want it unchanged", got)
			}
			st, err := f.Stage()
			if err != nil {
				t.Fatal(err)
			}
			if st.Name != "shape pv" || len(st.Allow) != 0 || len(st.Files) != 0 {
				t.Errorf("stage %+v", st)
			}
			if !slices.Equal(st.Argv, tt.argv) {
				t.Errorf("argv %q, want %q", st.Argv, tt.argv)
			}
		})
	}
}

func TestPvErrors(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, text, wantErr string }{
		{"rate-limit empty", "rate-limit: ''", "line 1: rate-limit must not be empty"},
		{"rate-limit null", "rate-limit: ~", "rate-limit has no value"},
		{"rate-limit list", "rate-limit: [1m]", "cannot unmarshal"},
		{"cmd a string", "cmd: pv -q", "a command must be a list"},
		{"unknown key", "rate: 1m", `unknown key "rate"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Classes["pv"](section(t, tt.text))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
