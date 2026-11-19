package dst

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Plan is how many archives of each period a destination keeps. A period
// with 0 or less is not made and its existing archives are never deleted.
// Config loading rejects negative counts.
type Plan struct {
	Y, M, W, D int
}

// periodOrder lists the periods from the longest.
const periodOrder = "ymwd"

// periodDays is the age in days at which the last archive of a period makes
// the next one due.
func periodDays(period string) int {
	switch period {
	case "y":
		return 365
	case "m":
		return 30
	case "w":
		return 7
	}
	return 1
}

func (p Plan) keep(period string) int {
	switch period {
	case "y":
		return p.Y
	case "m":
		return p.M
	case "w":
		return p.W
	case "d":
		return p.D
	}
	return 0
}

// Namer names archives of one profile and decides which of them to delete.
// It works on file names only: listing and deleting files is the caller's job.
type Namer struct {
	Prefix string
	// Suffix is the full suffix of new archives, such as "tar.gz.enc".
	Suffix string
	Plan   Plan
	// NoRotate leaves rotation to someone else: Rotate deletes nothing.
	NoRotate bool
}

type archive struct {
	name   string
	date   time.Time
	period string
}

const dateLayout = "2006-01-02"

// pattern matches the start of an archive name. Only the main suffix, the
// part before the first dot, is matched, so archives made before compression
// or encryption changed are still recognised.
func (n Namer) pattern() *regexp.Regexp {
	main, _, _ := strings.Cut(n.Suffix, ".")
	return regexp.MustCompile(`^` + regexp.QuoteMeta(n.Prefix) +
		`-([0-9]{4}-[0-9]{2}-[0-9]{2})-([ymwd])\.` + regexp.QuoteMeta(main))
}

// parse returns the archive name refers to. A name with a date that does not
// exist is not an archive.
func parse(re *regexp.Regexp, name string) (archive, bool) {
	m := re.FindStringSubmatch(name)
	if m == nil {
		return archive{}, false
	}
	date, err := time.Parse(dateLayout, m[1])
	if err != nil {
		return archive{}, false
	}
	return archive{name: name, date: date, period: m[2]}, true
}

// archives returns the archives among names, sorted by name.
func archives(re *regexp.Regexp, names []string) []archive {
	var out []archive
	for _, name := range names {
		if a, ok := parse(re, name); ok {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b archive) int { return strings.Compare(a.name, b.name) })
	return out
}

// Archives returns the names of the archives among names, sorted by name.
func (n Namer) Archives(names []string) []string {
	var out []string
	for _, a := range archives(n.pattern(), names) {
		out = append(out, a.name)
	}
	return out
}

func civilDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func (n Namer) name(date time.Time, period string) string {
	return fmt.Sprintf("%s-%s-%s.%s", n.Prefix, date.Format(dateLayout), period, n.Suffix)
}

// Next returns the name of the archive to write today, given the names
// already in the destination. Only the calendar date of today in its own
// location is used, so the caller passes local time.
//
// If the last archive is dated today, the new one takes its period and
// replaces it. Otherwise it gets the longest period of the plan that has no
// archive yet or whose last archive is old enough, and failing that the
// shortest period of the plan.
func (n Namer) Next(names []string, today time.Time) (string, error) {
	today = civilDate(today)
	all := archives(n.pattern(), names)
	if len(all) > 0 {
		if last := all[len(all)-1]; last.date.Equal(today) {
			return n.name(today, last.period), nil
		}
	}
	next := ""
	for _, r := range periodOrder {
		period := string(r)
		if n.Plan.keep(period) <= 0 {
			continue
		}
		next = period
		last, ok := lastOf(all, period)
		if !ok || !last.date.After(today.AddDate(0, 0, -periodDays(period))) {
			break
		}
	}
	if next == "" {
		return "", errors.New("plan has no period with a positive count")
	}
	return n.name(today, next), nil
}

func lastOf(all []archive, period string) (archive, bool) {
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].period == period {
			return all[i], true
		}
	}
	return archive{}, false
}

// split sorts the archives among names, other than written, into those
// written replaces and the rest.
func (n Namer) split(names []string, written string) (w archive, replaced, rest []archive, err error) {
	re := n.pattern()
	w, ok := parse(re, written)
	if !ok {
		return archive{}, nil, nil, fmt.Errorf("%q is not an archive name of prefix %q", written, n.Prefix)
	}
	for _, a := range archives(re, names) {
		switch {
		case a.name == written:
		case a.period == w.period && a.date.Equal(w.date):
			replaced = append(replaced, a)
		default:
			rest = append(rest, a)
		}
	}
	return w, replaced, rest, nil
}

// Replaced returns the archives that written replaces: those with its date
// and period under another name, left by an earlier run the same day under
// an older suffix. The caller deletes them after written is in place, with
// or without no-rotate.
func (n Namer) Replaced(names []string, written string) ([]string, error) {
	_, replaced, _, err := n.split(names, written)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range replaced {
		out = append(out, a.name)
	}
	return out, nil
}

// Rotate returns the archives to delete to bring each period of the plan
// down to its count once written is in place, oldest first. written counts
// as present even if names lacks it and is never returned; the archives it
// replaces are left to Replaced and not counted. Archives of a period not in
// the plan are kept, and with NoRotate all of them.
func (n Namer) Rotate(names []string, written string) ([]string, error) {
	w, _, rest, err := n.split(names, written)
	if err != nil {
		return nil, err
	}
	if n.NoRotate {
		return nil, nil
	}
	count := map[string]int{w.period: 1}
	for _, a := range rest {
		count[a.period]++
	}
	var del []string
	for _, r := range periodOrder {
		period := string(r)
		keep := n.Plan.keep(period)
		if keep <= 0 {
			continue
		}
		excess := count[period] - keep
		for _, a := range rest {
			if excess <= 0 {
				break
			}
			if a.period == period {
				del = append(del, a.name)
				excess--
			}
		}
	}
	return del, nil
}
