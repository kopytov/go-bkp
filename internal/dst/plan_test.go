package dst

import (
	"slices"
	"testing"
	"time"
)

// today is 2026-11-18. 365 days earlier is 2025-11-18, 30 days earlier is
// 2026-10-19, 7 days earlier is 2026-11-11.
var today = time.Date(2026, 11, 18, 23, 59, 0, 0, time.Local)

const (
	prefix = "mysql"
	suffix = "sql.gz"
)

func TestNext(t *testing.T) {
	t.Parallel()
	full := Plan{Y: 1, M: 3, W: 4, D: 7}
	tests := []struct {
		name  string
		plan  Plan
		names []string
		want  string
	}{
		{"empty, full plan", full, nil, "mysql-2026-11-18-y.sql.gz"},
		{"empty, only d", Plan{D: 7}, nil, "mysql-2026-11-18-d.sql.gz"},
		{"empty, y is 0", Plan{Y: 0, M: 1, D: 1}, nil, "mysql-2026-11-18-m.sql.gz"},
		{"y due exactly", full, []string{"mysql-2025-11-18-y.sql.gz"}, "mysql-2026-11-18-y.sql.gz"},
		{"y a day short, no m yet", full, []string{"mysql-2025-11-19-y.sql.gz"}, "mysql-2026-11-18-m.sql.gz"},
		{"m due exactly", full, []string{
			"mysql-2025-11-19-y.sql.gz", "mysql-2026-10-19-m.sql.gz",
		}, "mysql-2026-11-18-m.sql.gz"},
		{"m a day short, no w yet", full, []string{
			"mysql-2025-11-19-y.sql.gz", "mysql-2026-10-20-m.sql.gz",
		}, "mysql-2026-11-18-w.sql.gz"},
		{"w due exactly", full, []string{
			"mysql-2025-11-19-y.sql.gz", "mysql-2026-10-20-m.sql.gz", "mysql-2026-11-11-w.sql.gz",
		}, "mysql-2026-11-18-w.sql.gz"},
		{"w a day short, d due", full, []string{
			"mysql-2025-11-19-y.sql.gz", "mysql-2026-10-20-m.sql.gz",
			"mysql-2026-11-12-w.sql.gz", "mysql-2026-11-17-d.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz"},
		{"nothing due, shortest planned period", Plan{Y: 1, W: 2}, []string{
			"mysql-2026-01-10-y.sql.gz", "mysql-2026-11-15-w.sql.gz",
		}, "mysql-2026-11-18-w.sql.gz"},
		{"second run same day keeps period", full, []string{
			"mysql-2026-11-17-d.sql.gz", "mysql-2026-11-18-m.sql.gz",
		}, "mysql-2026-11-18-m.sql.gz"},
		{"second run same day, older suffix", full, []string{
			"mysql-2026-11-18-m.sql",
		}, "mysql-2026-11-18-m.sql.gz"},
		{"second run same day, period dropped from plan", Plan{D: 7}, []string{
			"mysql-2026-11-18-y.sql.gz",
		}, "mysql-2026-11-18-y.sql.gz"},
		{"other prefix dated today is not ours", Plan{Y: 1, D: 7}, []string{
			"mysql-2026-01-10-y.sql.gz", "mysql-wp-2026-11-18-y.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz"},
		{"partial dated today is not an archive", Plan{Y: 1, D: 7}, []string{
			"mysql-2026-01-10-y.sql.gz", ".mysql-2026-11-18-y.sql.gz.partial",
		}, "mysql-2026-11-18-d.sql.gz"},
		{"older full suffix still counts", Plan{Y: 1, D: 7}, []string{
			"mysql-2026-01-10-y.sql",
		}, "mysql-2026-11-18-d.sql.gz"},
		{"other main suffix does not count", Plan{Y: 1, D: 7}, []string{
			"mysql-2026-01-10-y.tar.gz",
		}, "mysql-2026-11-18-y.sql.gz"},
		{"impossible date is not an archive", Plan{Y: 1, D: 7}, []string{
			"mysql-2026-02-30-y.sql.gz",
		}, "mysql-2026-11-18-y.sql.gz"},
		{"unknown period is not an archive", Plan{Y: 1, D: 7}, []string{
			"mysql-2026-11-18-x.sql.gz", "mysql-2026-01-10-y.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := Namer{Prefix: prefix, Suffix: suffix, Plan: tt.plan}
			got, err := n.Next(tt.names, today)
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if got != tt.want {
				t.Errorf("Next = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNextEmptyPlan(t *testing.T) {
	t.Parallel()
	n := Namer{Prefix: prefix, Suffix: suffix}
	if got, err := n.Next(nil, today); err == nil {
		t.Errorf("Next = %q, want an error", got)
	}
}

func TestNextUsesCalendarDate(t *testing.T) {
	t.Parallel()
	n := Namer{Prefix: prefix, Suffix: suffix, Plan: Plan{W: 1, D: 7}}
	for _, at := range []time.Time{
		time.Date(2026, 11, 18, 0, 0, 1, 0, time.Local),
		time.Date(2026, 11, 18, 23, 59, 59, 0, time.Local),
	} {
		for _, tt := range []struct {
			names []string
			want  string
		}{
			{[]string{"mysql-2026-11-11-w.sql.gz"}, "mysql-2026-11-18-w.sql.gz"},
			{[]string{"mysql-2026-11-12-w.sql.gz"}, "mysql-2026-11-18-d.sql.gz"},
			{[]string{"mysql-2026-11-17-w.sql.gz", "mysql-2026-11-18-d.sql.gz"}, "mysql-2026-11-18-d.sql.gz"},
		} {
			got, err := n.Next(tt.names, at)
			if err != nil {
				t.Fatalf("Next(%q, %v): %v", tt.names, at, err)
			}
			if got != tt.want {
				t.Errorf("Next(%q, %v) = %q, want %q", tt.names, at, got, tt.want)
			}
		}
	}
}

func TestRotate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		plan    Plan
		names   []string
		written string
		want    []string
	}{
		{"within plan", Plan{D: 7}, []string{
			"mysql-2026-11-16-d.sql.gz", "mysql-2026-11-17-d.sql.gz", "mysql-2026-11-18-d.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz", nil},
		{"one over", Plan{D: 3}, []string{
			"mysql-2026-11-15-d.sql.gz", "mysql-2026-11-16-d.sql.gz",
			"mysql-2026-11-17-d.sql.gz", "mysql-2026-11-18-d.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz", []string{"mysql-2026-11-15-d.sql.gz"}},
		{"several over in several periods", Plan{W: 2, D: 1}, []string{
			"mysql-2026-10-21-w.sql.gz", "mysql-2026-10-28-w.sql.gz", "mysql-2026-11-04-w.sql.gz",
			"mysql-2026-11-16-d.sql.gz", "mysql-2026-11-17-d.sql.gz", "mysql-2026-11-18-d.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz", []string{
			"mysql-2026-10-21-w.sql.gz", "mysql-2026-11-16-d.sql.gz", "mysql-2026-11-17-d.sql.gz",
		}},
		{"periods out of plan are kept", Plan{Y: 0, D: 1}, []string{
			"mysql-2025-01-01-y.sql.gz", "mysql-2026-01-01-y.sql.gz", "mysql-2026-10-01-m.sql.gz",
			"mysql-2026-11-17-d.sql.gz", "mysql-2026-11-18-d.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz", []string{"mysql-2026-11-17-d.sql.gz"}},
		{"written missing from names still counts", Plan{D: 1}, []string{
			"mysql-2026-11-17-d.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz", []string{"mysql-2026-11-17-d.sql.gz"}},
		{"first archive of its period", Plan{M: 1, D: 2}, []string{
			"mysql-2026-11-16-d.sql.gz", "mysql-2026-11-17-d.sql.gz",
		}, "mysql-2026-11-18-m.sql.gz", nil},
		{"replaced archive is left to Replaced and not counted", Plan{D: 2}, []string{
			"mysql-2026-11-17-d.sql", "mysql-2026-11-18-d.sql", "mysql-2026-11-18-d.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz", nil},
		{"same day other period is not replaced", Plan{Y: 1, D: 7}, []string{
			"mysql-2026-11-18-d.sql.gz", "mysql-2026-11-18-y.sql.gz",
		}, "mysql-2026-11-18-y.sql.gz", nil},
		{"foreign files are never deleted", Plan{D: 1}, []string{
			"notes.txt", ".mysql-2026-11-10-d.sql.gz.partial", "mysql-wp-2026-11-10-d.sql.gz",
			"mysql-2026-11-10-d.tar.gz", "mysql-2026-02-30-d.sql.gz", "mysql-2026-11-10-x.sql.gz",
			"mysql-2026-11-17-d.sql.gz", "mysql-2026-11-18-d.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz", []string{"mysql-2026-11-17-d.sql.gz"}},
		{"older full suffix counts toward the plan", Plan{D: 1}, []string{
			"mysql-2026-11-17-d.sql", "mysql-2026-11-18-d.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz", []string{"mysql-2026-11-17-d.sql"}},
		{"written is kept even when not the newest", Plan{D: 1}, []string{
			"mysql-2026-11-18-d.sql.gz", "mysql-2026-11-19-d.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz", []string{"mysql-2026-11-19-d.sql.gz"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := Namer{Prefix: prefix, Suffix: suffix, Plan: tt.plan}
			got, err := n.Rotate(tt.names, tt.written)
			if err != nil {
				t.Fatalf("Rotate: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Rotate = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRotateNoRotate(t *testing.T) {
	t.Parallel()
	n := Namer{Prefix: prefix, Suffix: suffix, Plan: Plan{D: 1}, NoRotate: true}
	names := []string{"mysql-2026-11-16-d.sql.gz", "mysql-2026-11-17-d.sql.gz"}
	got, err := n.Rotate(names, "mysql-2026-11-18-d.sql.gz")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if got != nil {
		t.Errorf("Rotate = %q, want nothing", got)
	}
	if _, err := n.Rotate(nil, "mysql-wp-2026-11-18-d.sql.gz"); err == nil {
		t.Error("Rotate accepted a foreign name with NoRotate")
	}
}

func TestReplaced(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		names   []string
		written string
		want    []string
	}{
		{"first run of the day", []string{
			"mysql-2026-11-17-d.sql.gz", "mysql-2026-11-18-d.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz", nil},
		{"same day under older suffix", []string{
			"mysql-2026-11-17-d.sql", "mysql-2026-11-18-d.sql", "mysql-2026-11-18-d.sql.gz",
		}, "mysql-2026-11-18-d.sql.gz", []string{"mysql-2026-11-18-d.sql"}},
		{"same day other period is kept", []string{
			"mysql-2026-11-18-d.sql", "mysql-2026-11-18-y.sql.gz",
		}, "mysql-2026-11-18-y.sql.gz", nil},
		{"other prefix and main suffix are kept", []string{
			"mysql-wp-2026-11-18-d.sql", "mysql-2026-11-18-d.tar", ".mysql-2026-11-18-d.sql.partial",
		}, "mysql-2026-11-18-d.sql.gz", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Replaced does not depend on the plan, so it is left empty.
			n := Namer{Prefix: prefix, Suffix: suffix}
			got, err := n.Replaced(tt.names, tt.written)
			if err != nil {
				t.Fatalf("Replaced: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Replaced = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRejectsForeignWritten(t *testing.T) {
	t.Parallel()
	n := Namer{Prefix: prefix, Suffix: suffix, Plan: Plan{D: 1}}
	for _, written := range []string{"mysql-wp-2026-11-18-d.sql.gz", "mysql-2026-02-30-d.sql.gz"} {
		if _, err := n.Rotate(nil, written); err == nil {
			t.Errorf("Rotate accepted %q", written)
		}
		if _, err := n.Replaced(nil, written); err == nil {
			t.Errorf("Replaced accepted %q", written)
		}
	}
}

func TestArchives(t *testing.T) {
	t.Parallel()
	n := Namer{Prefix: prefix, Suffix: suffix, Plan: Plan{D: 1}}
	got := n.Archives([]string{
		"mysql-2026-11-18-d.sql.gz", "notes.txt", "mysql-2025-11-18-y.sql",
		".mysql-2026-11-17-d.sql.gz.partial", "mysql-wp-2026-11-18-d.sql.gz",
		"mysql-2026-11-17-d.sql.gz",
	})
	want := []string{"mysql-2025-11-18-y.sql", "mysql-2026-11-17-d.sql.gz", "mysql-2026-11-18-d.sql.gz"}
	if !slices.Equal(got, want) {
		t.Errorf("Archives = %q, want %q", got, want)
	}
}
