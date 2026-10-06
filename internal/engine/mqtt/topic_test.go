package mqttengine

import "testing"

func TestTopicMatchesFilter(t *testing.T) {
	cases := []struct {
		topic, filter string
		want          bool
	}{
		{"sensors/kitchen/temp", "sensors/kitchen/temp", true},
		{"sensors/kitchen/temp", "sensors/+/temp", true},
		{"sensors/kitchen/temp", "sensors/+/humidity", false},
		{"sensors/kitchen/temp", "sensors/#", true},
		{"sensors", "sensors/#", true}, // per MQTT spec §4.7.1.2, "sport/#" also matches the parent level "sport" itself
		{"sensors/kitchen", "sensors/#", true},
		{"sensors/kitchen/temp/extra", "sensors/+/temp", false},
		{"a/b", "a/b/c", false},
		{"a/b/c", "a/b", false},
		{"cmd", "cmd", true},
		{"cmd", "+", true},
	}
	for _, c := range cases {
		got := topicMatchesFilter(c.topic, c.filter)
		if got != c.want {
			t.Errorf("topicMatchesFilter(%q, %q) = %v, want %v", c.topic, c.filter, got, c.want)
		}
	}
}
