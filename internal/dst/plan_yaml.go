package dst

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// UnmarshalYAML implements yaml.Unmarshaler. A plan is a mapping of the
// periods y, m, w and d to counts that are not negative, at least one of
// them positive. As in the Raku version, a count may be a quoted number and
// a period without a value counts as 0.
func (p *Plan) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: plan must be a mapping of y, m, w, d to counts", n.Line)
	}
	var out Plan
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		var field *int
		switch k.Value {
		case "y":
			field = &out.Y
		case "m":
			field = &out.M
		case "w":
			field = &out.W
		case "d":
			field = &out.D
		default:
			return fmt.Errorf("line %d: plan: unknown period %q, want y, m, w or d", k.Line, k.Value)
		}
		if seen[k.Value] {
			return fmt.Errorf("line %d: plan: period %q given twice", k.Line, k.Value)
		}
		seen[k.Value] = true
		for v.Kind == yaml.AliasNode && v.Alias != nil {
			v = v.Alias
		}
		count, err := planCount(v)
		if err != nil {
			return fmt.Errorf("line %d: plan.%s: %w", v.Line, k.Value, err)
		}
		*field = count
		if count < 0 {
			return fmt.Errorf("line %d: plan.%s: %d is negative", v.Line, k.Value, *field)
		}
	}
	if out == (Plan{}) {
		return errors.New("plan has no period with a positive count")
	}
	*p = out
	return nil
}

func planCount(v *yaml.Node) (int, error) {
	if v.Kind != yaml.ScalarNode {
		return 0, errors.New("want a whole number")
	}
	switch v.ShortTag() {
	case "!!null":
		return 0, nil
	case "!!int":
		var n int
		if err := v.Decode(&n); err != nil {
			return 0, err
		}
		return n, nil
	case "!!str":
		n, err := strconv.Atoi(strings.TrimSpace(v.Value))
		if err != nil {
			return 0, fmt.Errorf("want a whole number, got %q", v.Value)
		}
		return n, nil
	}
	return 0, fmt.Errorf("want a whole number, got %q", v.Value)
}
