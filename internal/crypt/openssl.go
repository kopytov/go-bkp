package crypt

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/kopytov/go-bkp/internal/config"
	"github.com/kopytov/go-bkp/internal/pipeline"
)

// maxKey is the longest key openssl reads whole from -pass fd:N: it reads
// one line into a buffer of 1024 bytes.
const maxKey = 1023

// cipherName is the form of a cipher name, as in Raku.
var cipherName = regexp.MustCompile(`^[a-z0-9-]+$`)

// Openssl is crypt class openssl: openssl enc with a password. The key
// goes to openssl on descriptor 3, never in argv, and the archive decrypts
// with openssl enc -d -<cipher> -k <key>, like an archive the Raku version
// wrote.
type Openssl struct {
	Cmd       config.Command `yaml:"cmd"`
	AddSuffix config.Suffix  `yaml:"add-suffix"`
	Key       Key            `yaml:"key"`
	Cipher    Cipher         `yaml:"cipher"`
}

// NewOpenssl builds an Openssl from its config section.
func NewOpenssl(n *yaml.Node) (*Openssl, error) {
	o := Openssl{Cmd: config.Command{"openssl", "enc"}, AddSuffix: "enc", Cipher: "aes-256-cbc"}
	if err := config.Decode(n, &o); err != nil {
		return nil, err
	}
	if o.Key == "" {
		return nil, fmt.Errorf("line %d: key is required", n.Line)
	}
	return &o, nil
}

// Suffix implements config.Filter.
func (o *Openssl) Suffix(in string) string { return in + "." + string(o.AddSuffix) }

// Stage implements config.Filter. The key is written to a pipe whose read
// end becomes descriptor 3 of the stage; it fits in the pipe buffer, so
// the write does not wait for openssl.
func (o *Openssl) Stage() (pipeline.Stage, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return pipeline.Stage{}, fmt.Errorf("key pipe: %w", err)
	}
	_, werr := w.WriteString(string(o.Key) + "\n")
	cerr := w.Close()
	if werr != nil || cerr != nil {
		r.Close() //nolint:errcheck,gosec // a pipe end only read from: nothing to lose
		return pipeline.Stage{}, fmt.Errorf("write key to pipe: %w", errors.Join(werr, cerr))
	}
	argv := append(slices.Clone(o.Cmd), "-"+string(o.Cipher), "-pass", "fd:3")
	return pipeline.Stage{Name: "crypt openssl", Argv: argv, Files: []*os.File{r}}, nil
}

// Key is the password of openssl enc. Its errors and its String never show
// the value.
type Key string

// UnmarshalYAML implements yaml.Unmarshaler.
func (k *Key) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: key must be a string", n.Line)
	}
	v := n.Value
	switch {
	case v == "":
		return fmt.Errorf("line %d: key must not be empty", n.Line)
	case strings.ContainsAny(v, "\n\x00"):
		return fmt.Errorf("line %d: key must not contain a line break or a NUL byte", n.Line)
	case len(v) > maxKey:
		return fmt.Errorf("line %d: key is %d bytes long, openssl takes at most %d", n.Line, len(v), maxKey)
	}
	*k = Key(v)
	return nil
}

// String implements fmt.Stringer, so that printing a class shows no key.
func (k Key) String() string { return "<hidden>" }

// GoString implements fmt.GoStringer, for %#v.
func (k Key) GoString() string { return "<hidden>" }

// Cipher is the name of an openssl cipher, such as aes-256-cbc.
type Cipher string

// UnmarshalYAML implements yaml.Unmarshaler.
func (c *Cipher) UnmarshalYAML(n *yaml.Node) error {
	var v string
	if err := n.Decode(&v); err != nil {
		return err
	}
	if !cipherName.MatchString(v) {
		return fmt.Errorf("line %d: cipher %q must hold only a-z, 0-9 and -", n.Line, v)
	}
	*c = Cipher(v)
	return nil
}
