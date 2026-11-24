package quality

// RubricDimensionSpec describes one dimension on the anchored 0-2 ordinal scale.
type RubricDimensionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	SupportsNA  bool           `json:"supports_na"`
	Level0      string         `json:"level_0"`
	Level1      string         `json:"level_1"`
	Level2      string         `json:"level_2"`
	Anchors     []AnchorSample `json:"anchors"`
}

// AnchorSample illustrates how a concrete explanation or resource maps to 0, 1, or 2.
type AnchorSample struct {
	Score   int    `json:"score"`
	Summary string `json:"summary"`
}

// AnchoredRubric returns the canonical 0-2 ordinal rubric specification for AP CS A.
func AnchoredRubric() []RubricDimensionSpec {
	return []RubricDimensionSpec{
		{
			Name:        "factual_accuracy",
			Description: "Technical correctness of Java semantics, types, bounds, API contracts, and AP CS A Fall 2025 scope.",
			Level0:      "Materially wrong or unusable: contains false Java behavior, invalid preconditions, or harmful misconceptions.",
			Level1:      "Partly supported or incomplete: no fatal falsehood, yet omits critical edge conditions or blurs scope boundaries.",
			Level2:      "Supported and usable: all technical claims align with Java semantics and cited AP CS A learning objectives.",
			Anchors: []AnchorSample{
				{Score: 0, Summary: "CAL-02 claims !(a && b) == !a && !b."},
				{Score: 1, Summary: "A shortened CAL-01 answer explains truncation and omits the effect of casting before division."},
				{Score: 2, Summary: "CAL-01 contrasts 7 / 2 with (double) 7 / 2 and explains why the results differ."},
			},
		},
		{
			Name:        "prerequisite_rationale",
			Description: "Pedagogical soundness of why prerequisite topics precede or prepare the learner for the failed concept.",
			Level0:      "Materially wrong or unusable: reverses prerequisite dependency order or inserts unrelated/out-of-scope prerequisites.",
			Level1:      "Partly supported or incomplete: lists valid graph prerequisites without explaining how they prepare the target concept.",
			Level2:      "Supported and usable: accurately orders and connects prerequisites to the learner's target concept.",
			Anchors: []AnchorSample{
				{Score: 0, Summary: "CAL-03 requires writing overloaded constructors before understanding what an object is."},
				{Score: 1, Summary: "A shortened CAL-01 answer names Data Types and Expressions without explaining their relationship."},
				{Score: 2, Summary: "CAL-01 explains why operand types matter for tracing division."},
			},
		},
		{
			Name:        "actionable_guidance",
			Description: "Specificity and clarity of concrete study steps, tracing exercises, or code checks for the learner.",
			Level0:      "Materially wrong or unusable: generic platitudes, self-contradictory instructions, or guidance executing defective logic.",
			Level1:      "Partly supported or incomplete: offers reasonable study direction but lacks concrete trace inputs or checks.",
			Level2:      "Supported and usable: gives concrete, bounded Java tracing or coding steps directly targeting the misconception.",
			Anchors: []AnchorSample{
				{Score: 0, Summary: "A CAL-04 variant says only to practice Java."},
				{Score: 1, Summary: "CAL-03 suggests coding a constructor without a concrete input or check."},
				{Score: 2, Summary: "CAL-05 asks the learner to trace a specific substring call on axiom."},
			},
		},
		{
			Name:        "resource_relevance",
			Description: "Evaluated individually per supplied resource for topic match, AP CS A level fit, and presentation preference alignment.",
			SupportsNA:  true,
			Level0:      "Materially wrong or unusable: resource covers an unrelated subject or contradicts the VARK presentation profile.",
			Level1:      "Partly supported or incomplete: broadly adjacent CS material that only partially addresses the failed topic.",
			Level2:      "Supported and usable: directly teaches the target AP CS A topic in the requested VARK modality.",
			Anchors: []AnchorSample{
				{Score: 0, Summary: "CAL-04 links to a database normal forms podcast for an ArrayList task."},
				{Score: 1, Summary: "CAL-01's video illustrates precedence with int operands only."},
				{Score: 2, Summary: "CAL-01's diagram directly illustrates mixed-type division."},
			},
		},
	}
}
