# AP Computer Science A offline quality benchmark

The benchmark covers English explanations and Java examples across all four units of the Fall 2025 AP Computer Science A framework. It ships 28 designated synthetic cases, seven per unit, and six separate calibration examples. Cases include validly structured false explanations, reversed prerequisites, irrelevant and absent resources, underspecified requests, and responses at the service limits. The 26 reference facts carry `provisional_educator_review_pending` until qualified educator review. [Course and exam description](https://apcentral.collegeboard.org/media/pdf/ap-computer-science-a-course-and-exam-description.pdf) · [Published clarifications](https://apcentral.collegeboard.org/media/pdf/ap-computer-science-a-course-and-exam-description-clarification.pdf)

## Files

| Path | Contents |
| --- | --- |
| [packets/](packets/) | Rater instructions, a calibration-only anchored rubric, the calibration packet, two separately fillable blind sheets, and a blank adjudication sheet. |
| [testdata/objectives.json](testdata/objectives.json) | All 73 course objectives, with unit counts of 28, 14, 10, and 21 and printed and PDF page locators. |
| [testdata/reference_facts.json](testdata/reference_facts.json) | Provisional reference facts, relevant source locators, and selected short Java assertions. |
| [testdata/cases.json](testdata/cases.json) | Synthetic prompts and plans, original offline resource excerpts, graph source, and provenance. |
| [testdata/identity_key.json](testdata/identity_key.json) | Private mapping to case IDs, providers, models, variants, seeded labels, and material digests. |
| [testdata/synthetic_completed_ratings.json](testdata/synthetic_completed_ratings.json) | Completed synthetic submissions for reproducing scorer arithmetic and gate rules. |
| [testdata/candidate_score_report_blank.json](testdata/candidate_score_report_blank.json) | Published unmeasured report with zero independent specialist ratings. |

## Rating and provenance

Give raters the files under [packets/](packets/) and keep the case inventory and identity key separate until both initial ratings are locked. The packet builder uses seeded randomized order and binds each opaque ID to the prompt, preference, failed and mastered topics, plan items, resource evidence, and provisional source locators. Resource URLs are placeholders. Raters can assess supplied resources from their locally included descriptions or excerpts. Suite validation and packet generation require at least 24 designated cases, at least six in each unit, and unique case IDs and material across calibration and evaluation. The material check compares what raters see in both sets, excluding the calibration scores and rationale. The builder and the exported packet audit check decoded JSON field names and string values for private case IDs, providers, models, variants, and every scenario label from the identity key. The builder names the affected record if it finds a disclosure.

Two qualified AP Computer Science A specialists complete calibration and rate independently before discussion. The **Lead CS Curriculum Adjudicator** preserves both locked ratings, including their notes and reasons, and records each final rating. Differences in scores, statuses, or critical judgments count as disagreements. A disagreement or an override of an agreed decision requires a recorded resolution reason.

The organizer retains the original identity key and provenance. `Evaluate` checks that both rating sheets and the adjudication agree on dataset kind and match that key, and that the key lists each case ID and its material once. Candidate keys must meet the same course coverage requirements as the packet. The `unmeasured_blank` kind is for empty sheets. A submission for an unseen candidate uses `measured_candidate`, a freshly generated packet, and explicit `unseen_candidate` provenance with model version or date and permission to retain the outputs. For any live sampling, set an explicit case cap and cost cap before collection and strip learner identifiers before storage. See [the rubric](packets/rubric.md) for scores, abstentions, and critical judgments.

## Candidate gate and reporting

The gate requires zero adjudicated critical errors, a factual pass fraction of at least `0.90`, and linear-weighted Cohen's κ of at least `0.70` for initial guidance ratings. Factual passes are scores of 2 among assessable final ratings. Agreement counts decide the inclusive κ comparison, so a result of exactly 0.70 meets the threshold. The scorer computes a 95% Wilson interval for the pass fraction and a 95% paired case-bootstrap interval for κ using 2,000 seeded resamples by default.

The report shows exact counts by unit, topic, presentation preference, and provider. It counts supplied resource slots and empty resource lists from the original material, including in progress reports. Initial guidance agreement uses both locked sheets and is available before any final adjudication.

A stratum or overall result with fewer than `10` assessable factual cases or paired guidance ratings, incomplete adjudication, or an undefined κ or bootstrap interval is inconclusive. The overall gate still fails when a valid adjudicated critical error is present. An inconclusive stratum retains its exact critical error count.

The synthetic completed sheets exercise the calculations. The published candidate report remains unmeasured. Candidate rankings, teaching quality and learning gains require additional evidence.

## Shipped graph coverage

The embedded seed graph can serve 8 of the 28 designated requests. The shipped example computer science graph can serve 22. Each servable case names its reference graph in `response_graph`, using the example graph when available and the seed graph otherwise. The checked-in items match the service output from that reference graph, including the canonical prerequisite chain. Cases outside both graphs use hand-authored plan items, with `response_graph` set to `none`.

## Offline verification

```sh
go test -race -count=1 ./evaluation/quality/...
```

The pure-Go quality package tests regenerate the blind artifacts, run the service with an offline fixture planner, and check scoring and schema rejection without network calls. The fixtures use the existing HTTP and MCP plan contracts.
