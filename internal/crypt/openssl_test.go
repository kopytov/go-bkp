package crypt

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/pipeline"
)

// key is the key of the tests; nothing else in them contains it.
const key = "secret"

// section parses the YAML of a config section without its class key.
func section(t *testing.T, text string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("unmarshal %q: %v", text, err)
	}
	return doc.Content[0]
}

// readKey reads what the stage gets on descriptor 3 and closes it.
func readKey(t *testing.T, st pipeline.Stage) string {
	t.Helper()
	if len(st.Files) != 1 {
		t.Fatalf("files %v, want the key pipe alone", st.Files)
	}
	defer st.Files[0].Close() //nolint:errcheck // a pipe end only read from
	b, err := io.ReadAll(st.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, text string
		argv       []string
		suffix     string
	}{
		{
			name: "defaults", text: "key: " + key,
			argv: []string{"openssl", "enc", "-aes-256-cbc", "-pass", "fd:3"}, suffix: "tar.gz.enc",
		},
		{
			name: "all attributes", text: "key: " + key + "\ncipher: chacha20\nadd-suffix: aes",
			argv: []string{"openssl", "enc", "-chacha20", "-pass", "fd:3"}, suffix: "tar.gz.aes",
		},
		{
			name: "cmd", text: "key: " + key + "\ncmd: [/opt/openssl/bin/openssl, enc, -salt]",
			argv: []string{"/opt/openssl/bin/openssl", "enc", "-salt", "-aes-256-cbc", "-pass", "fd:3"}, suffix: "tar.gz.enc",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, err := Classes["openssl"](section(t, tt.text))
			if err != nil {
				t.Fatal(err)
			}
			if got := f.Suffix("tar.gz"); got != tt.suffix {
				t.Errorf("suffix %q, want %q", got, tt.suffix)
			}
			st, err := f.Stage()
			if err != nil {
				t.Fatal(err)
			}
			if got := readKey(t, st); got != key+"\n" {
				t.Errorf("descriptor 3 got %q, want the key and a line break", got)
			}
			if st.Name != "crypt openssl" || len(st.Allow) != 0 {
				t.Errorf("name %q, allow %v", st.Name, st.Allow)
			}
			if !slices.Equal(st.Argv, tt.argv) {
				t.Errorf("argv %q, want %q", st.Argv, tt.argv)
			}
			for _, a := range st.Argv {
				if strings.Contains(a, key) {
					t.Errorf("key in argv %q", st.Argv)
				}
			}
			for _, s := range []string{fmt.Sprint(f), fmt.Sprintf("%+v", f), fmt.Sprintf("%#v", f)} {
				if strings.Contains(s, key) {
					t.Errorf("key printed: %s", s)
				}
			}
		})
	}
}

// TestStageFresh checks that each attempt gets a pipe of its own with the
// whole key in it.
func TestStageFresh(t *testing.T) {
	t.Parallel()
	o, err := NewOpenssl(section(t, "key: "+key))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		st, err := o.Stage()
		if err != nil {
			t.Fatal(err)
		}
		if got := readKey(t, st); got != key+"\n" {
			t.Errorf("descriptor 3 got %q", got)
		}
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("k", maxKey+1)
	tests := []struct{ name, text, wantErr string }{
		{"no key", "cipher: aes-128-cbc", "line 1: key is required"},
		{"key null", "key: ~", "key has no value"},
		{"key empty", "key: ''", "key must not be empty"},
		{"key with a line break", `key: "` + key + `\nmore"`, "key must not contain a line break"},
		{"key with a NUL", `key: "` + key + `\0"`, "key must not contain a line break or a NUL byte"},
		{"key too long", "key: " + long, fmt.Sprintf("key is %d bytes long, openssl takes at most 1023", maxKey+1)},
		{"key a list", "key: [" + key + "]", "key must be a string"},
		{"cipher upper case", "key: " + key + "\ncipher: AES-256-CBC", `cipher "AES-256-CBC" must hold only`},
		{"cipher with a space", "key: " + key + "\ncipher: 'aes -in'", `cipher "aes -in" must hold only`},
		{"cipher empty", "key: " + key + "\ncipher: ''", `cipher "" must hold only`},
		{"cipher null", "key: " + key + "\ncipher: ~", "cipher has no value"},
		{"empty add-suffix", "key: " + key + "\nadd-suffix: ''", `suffix "" must not be empty`},
		{"unknown key", "key: " + key + "\npassword: x", `unknown key "password"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Classes["openssl"](section(t, tt.text))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), key) {
				t.Errorf("key in the error: %v", err)
			}
		})
	}
	if _, err := Classes["openssl"](section(t, "key: "+long[:maxKey])); err != nil {
		t.Errorf("key of %d bytes: %v", maxKey, err)
	}
}

// TestOpensslCompat checks with the real openssl that archives encrypted
// here decrypt the way the Raku README says, with the key in argv, and
// that what openssl encrypts with -k decrypts with the key on descriptor 3.
func TestOpensslCompat(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skipf("openssl not found: %v", err)
	}
	dir := t.TempDir()
	plain := bytes.Repeat([]byte("bkp archive\x00\xff"), 50_000)
	plainPath := filepath.Join(dir, "plain")
	if err := os.WriteFile(plainPath, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, cipher, key string }{
		{"aes-256-cbc", "aes-256-cbc", key},
		{"aes-128-cbc", "aes-128-cbc", key},
		{"longest key", "aes-256-cbc", strings.Repeat("k", maxKey)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			attrs := "key: " + tt.key + "\ncipher: " + tt.cipher
			base := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-"))

			// Encrypted here, decrypted with the key in argv, as Raku did.
			enc := base + ".new.enc"
			run(t, tt.key, attrs, plainPath, enc)
			got := openssl(t, "enc", "-d", "-"+tt.cipher, "-k", tt.key, "-in", enc)
			if !bytes.Equal(got, plain) {
				t.Errorf("openssl enc -d -k: got %d bytes, want the %d of the input", len(got), len(plain))
			}

			// Encrypted as Raku did, decrypted with the key on descriptor 3.
			old := base + ".old.enc"
			openssl(t, "enc", "-"+tt.cipher, "-k", tt.key, "-in", plainPath, "-out", old)
			dec := base + ".dec"
			run(t, tt.key, attrs+"\ncmd: [openssl, enc, -d]", old, dec)
			got, err := os.ReadFile(dec) //nolint:gosec // G304: a path under t.TempDir
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plain) {
				t.Errorf("decrypted with -pass fd:3: got %d bytes, want the %d of the input", len(got), len(plain))
			}
		})
	}
}

// run passes the file in through the openssl class configured by text into
// the file out, and checks that openssl did not print key.
func run(t *testing.T, key, text, in, out string) {
	t.Helper()
	o, err := NewOpenssl(section(t, text))
	if err != nil {
		t.Fatal(err)
	}
	st, err := o.Stage()
	if err != nil {
		t.Fatal(err)
	}
	inF, err := os.Open(in) //nolint:gosec // G304: a path under t.TempDir
	if err != nil {
		pipeline.Spec{Stages: []pipeline.Stage{st}}.Close()
		t.Fatal(err)
	}
	outF, err := os.Create(out) //nolint:gosec // G304: a path under t.TempDir
	if err != nil {
		pipeline.Spec{In: inF, Stages: []pipeline.Stage{st}}.Close()
		t.Fatal(err)
	}
	defer outF.Close() //nolint:errcheck // closed and checked below; this one is for a failed test
	var stderr bytes.Buffer
	p, err := pipeline.Start(t.Context(), pipeline.Spec{In: inF, Stages: []pipeline.Stage{st}, Out: outF, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := outF.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), key) {
		t.Errorf("key in the stderr of openssl: %s", stderr.String())
	}
}

// openssl runs the real openssl with args and returns its stdout.
func openssl(t *testing.T, args ...string) []byte {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(t.Context(), "openssl", args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("openssl %q: %v: %s", args, err, stderr.String())
	}
	return stdout.Bytes()
}
