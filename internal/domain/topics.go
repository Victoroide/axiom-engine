package domain

// Topic describes a concept and its direct prerequisite relationships.
type Topic struct {
	Name          string   `json:"name" example:"Recursion"`
	Prerequisites []string `json:"prerequisites" example:"Functions"`
	Dependents    []string `json:"dependents" example:"Dynamic Programming,Merge Sort"`
}

// TopicsResponse contains concepts sorted by their stored names.
type TopicsResponse struct {
	Topics []Topic `json:"topics"`
}

// TopicPathResponse contains a concept's prerequisites in study order.
// PrerequisiteChain excludes Topic; Dependents contains only direct dependents.
type TopicPathResponse struct {
	Topic             string   `json:"topic" example:"Recursion"`
	PrerequisiteChain []string `json:"prerequisite_chain" example:"Functions"`
	Dependents        []string `json:"dependents" example:"Dynamic Programming,Merge Sort"`
}
