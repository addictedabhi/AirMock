package mqttengine

import "strings"

// topicMatchesFilter implements MQTT topic-filter matching (§4.7): "+"
// matches exactly one level, "#" (only valid as the final level) matches
// that level and everything below it.
func topicMatchesFilter(topic, filter string) bool {
	topicLevels := strings.Split(topic, "/")
	filterLevels := strings.Split(filter, "/")

	for i, fl := range filterLevels {
		if fl == "#" {
			return true
		}
		if i >= len(topicLevels) {
			return false
		}
		if fl != "+" && fl != topicLevels[i] {
			return false
		}
	}
	return len(topicLevels) == len(filterLevels)
}
