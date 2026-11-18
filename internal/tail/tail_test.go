package tail

import "testing"

func TestBuffer(t *testing.T) {
	t.Parallel()
	b := New(5)
	if got := b.String(); got != "(empty)" {
		t.Errorf("empty: %q", got)
	}
	for _, s := range []string{"ab", "cd\n"} {
		if _, err := b.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if got := b.String(); got != "abcd" {
		t.Errorf("short: %q", got)
	}
	if _, err := b.Write([]byte("xyzабв")); err != nil {
		t.Fatal(err)
	}
	// The last 5 bytes start inside "а"; the cut moves to "б".
	if got := b.String(); got != "…бв" {
		t.Errorf("cut: %q", got)
	}
	long := New(5)
	if _, err := long.Write([]byte("xyzабв\n")); err != nil {
		t.Fatal(err)
	}
	if got := long.String(); got != "…бв" {
		t.Errorf("cut in String: %q", got)
	}
}
