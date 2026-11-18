package dst

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestPlanUnmarshalYAML(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		text    string
		want    Plan
		wantErr string
	}{
		{name: "full", text: "{y: 2, m: 6, w: 4, d: 6}", want: Plan{Y: 2, M: 6, W: 4, D: 6}},
		{name: "one period", text: "{d: 7}", want: Plan{D: 7}},
		{name: "zero beside positive", text: "{y: 0, d: 1}", want: Plan{D: 1}},
		{name: "empty mapping", text: "{}", wantErr: "no period with a positive count"},
		{name: "all zero", text: "{y: 0, d: 0}", wantErr: "no period with a positive count"},
		{name: "negative", text: "{m: -1, d: 3}", wantErr: "plan.m: -1 is negative"},
		{name: "unknown period", text: "{x: 1}", wantErr: `unknown period "x"`},
		{name: "upper case period", text: "{D: 1}", wantErr: `unknown period "D"`},
		{name: "twice", text: "{d: 1, d: 2}", wantErr: `period "d" given twice`},
		{name: "hex count", text: "{d: 0x10}", want: Plan{D: 16}},
		{name: "quoted count", text: `{d: "3"}`, want: Plan{D: 3}},
		{name: "empty count", text: "{y: , d: 2}", want: Plan{D: 2}},
		{name: "alias count", text: "{d: &n 2, w: *n}", want: Plan{W: 2, D: 2}},
		{name: "quoted negative", text: `{d: "-1"}`, wantErr: "plan.d: -1 is negative"},
		{name: "word count", text: "{d: six}", wantErr: `want a whole number, got "six"`},
		{name: "float count", text: "{d: 1.5}", wantErr: "want a whole number"},
		{name: "list count", text: "{d: [1]}", wantErr: "want a whole number"},
		{name: "not a mapping", text: "[1, 2]", wantErr: "plan must be a mapping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got struct {
				Plan Plan `yaml:"plan"`
			}
			err := yaml.Unmarshal([]byte("plan: "+tt.text), &got)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Plan != tt.want {
				t.Errorf("got %+v, want %+v", got.Plan, tt.want)
			}
		})
	}
}
