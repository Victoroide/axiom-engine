package domain

// TopicProgress reports completion of a topic call.
type TopicProgress struct {
	Topic          string `json:"topic"`
	Index          int    `json:"index"`
	ItemsGenerated int    `json:"items_generated"`
}
