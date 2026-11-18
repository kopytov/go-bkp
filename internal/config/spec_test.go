package config

import (
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/kopytov/go-bkp/internal/pipeline"
)

// fileSrc is a source read from a file, as redis is, or one failing with
// err.
type fileSrc struct {
	in  *os.File
	err error
}

func (*fileSrc) Suffix() string { return "rdb" }

func (s *fileSrc) Spec() (pipeline.Spec, error) {
	if s.err != nil {
		return pipeline.Spec{}, s.err
	}
	return pipeline.Spec{In: s.in}, nil
}

// stageFilter returns a fixed stage, or err.
type stageFilter struct {
	st  pipeline.Stage
	err error
}

func (*stageFilter) Suffix(in string) string { return in + ".x" }

func (f *stageFilter) Stage() (pipeline.Stage, error) { return f.st, f.err }

func TestProfileSpec(t *testing.T) {
	t.Parallel()
	p := Profile{
		Src:      &fakeSrc{fake{class: "tar"}},
		Compress: &fakeFilter{fake{class: "gzip"}},
		Crypt:    &fakeFilter{fake{class: "openssl"}},
		Shape:    &fakeFilter{fake{class: "pv"}},
	}
	s, err := p.Spec()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, st := range s.Stages {
		got = append(got, st.Name)
	}
	if want := []string{"src tar", "gzip", "openssl", "pv"}; !slices.Equal(got, want) {
		t.Errorf("stages %q, want %q", got, want)
	}

	p.Crypt = nil
	s, err = p.Spec()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Stages) != 3 || s.Stages[2].Name != "pv" {
		t.Errorf("without crypt: stages %+v", s.Stages)
	}
}

// TestProfileSpecCloses checks that a failing filter leaves no descriptor
// open that the source or an earlier filter opened.
func TestProfileSpecCloses(t *testing.T) {
	t.Parallel()
	in, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := inW.Close(); err != nil {
			t.Error(err)
		}
	})
	key, keyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := keyW.Close(); err != nil {
			t.Error(err)
		}
	})
	p := Profile{
		Src:   &fileSrc{in: in},
		Crypt: &stageFilter{st: pipeline.Stage{Argv: []string{"openssl"}, Files: []*os.File{key}}},
		Shape: &stageFilter{err: errors.New("broken")},
	}
	if _, err := p.Spec(); err == nil || err.Error() != "shape: broken" {
		t.Fatalf("err = %v, want shape: broken", err)
	}
	for _, f := range []*os.File{in, key} {
		if err := f.Close(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("%s left open: close = %v", f.Name(), err)
		}
	}
}

// TestProfileSpecSrcFails checks that a failing source asks no filter for
// its stage.
func TestProfileSpecSrcFails(t *testing.T) {
	t.Parallel()
	p := Profile{
		Src:      &fileSrc{err: errors.New("no dump")},
		Compress: &stageFilter{err: errors.New("asked")},
	}
	if _, err := p.Spec(); err == nil || err.Error() != "src: no dump" {
		t.Fatalf("err = %v, want src: no dump", err)
	}
}
