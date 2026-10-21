package kmsgx

import (
	"reflect"
	"testing"

	"github.com/shepherd-yun/datavim/internal/kafka"
)

func TestPlanConfigs(t *testing.T) {
	current := []kafka.ConfigEntry{
		{Name: "cleanup.policy", Value: "delete", Default: true},
		{Name: "retention.ms", Value: "604800000", Source: "topic"},
		{Name: "sasl.jaas.config", Sensitive: true},
	}
	for _, tc := range []struct {
		name  string
		edits []ConfigEdit
		want  []kafka.ConfigChange
		err   string
	}{
		{
			"set",
			[]ConfigEdit{{Name: "retention.ms", Value: "86400000"}},
			[]kafka.ConfigChange{{Name: "retention.ms", Value: "86400000", Old: "604800000"}},
			"",
		},
		{
			"set a default one",
			[]ConfigEdit{{Name: "cleanup.policy", Value: "compact"}},
			[]kafka.ConfigChange{{Name: "cleanup.policy", Value: "compact", Old: "delete", OldDefault: true}},
			"",
		},
		{"back to the same value", []ConfigEdit{{Name: "retention.ms", Value: "604800000"}}, nil, ""},
		{"delete a default", []ConfigEdit{{Name: "cleanup.policy", Delete: true}}, nil, ""},
		{
			"delete an override",
			[]ConfigEdit{{Name: "retention.ms", Delete: true}},
			[]kafka.ConfigChange{{Name: "retention.ms", Delete: true, Old: "604800000"}},
			"",
		},
		{
			"last edit wins",
			[]ConfigEdit{{Name: "retention.ms", Value: "1"}, {Name: "retention.ms", Delete: true}, {Name: "retention.ms", Value: "2"}},
			[]kafka.ConfigChange{{Name: "retention.ms", Value: "2", Old: "604800000"}},
			"",
		},
		{"unknown", []ConfigEdit{{Name: "nope", Value: "1"}}, nil, "unknown config nope"},
		{"sensitive", []ConfigEdit{{Name: "sasl.jaas.config", Value: "x"}}, nil, "sensitive configs can't be edited here"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PlanConfigs(current, tc.edits)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
