# Blind rating kit

Use the [rating rubric](rubric.md) and complete the six separate examples in [the calibration packet](calibration_packet.json). Raters should have AP Computer Science A Java instruction or grading experience under the Fall 2025 framework.

1. Use only your assigned [rater A](rater_a_blank.json) or [rater B](rater_b_blank.json) sheet. Record your qualification, qualification attestation, and calibration completion. Use the submitted packet's designated dataset kind when you start filling the sheet.
2. Work independently. Preserve the response order and supplied material. Read each learner prompt, response, provisional course locators, and offline resource evidence.
3. For `factual_accuracy`, `prerequisite_rationale`, and `actionable_guidance`, set `status` to `"scored"` with a score from 0 through 2, or use `"abstain"` with a reason.
4. Use `"applicable"` in `resource_summary_status` for responses with resources. Rate each supplied slot using its zero-based `item_index` and `resource_index`. Abstain with a reason if the evidence is unavailable. Use `"na"` and an empty `resource_ratings` array when the response has no resources.
5. Record `critical_error` as `true` or `false` with the corresponding `critical_error_type` on every completed rating.
6. Set `locked` to `true` when you submit your finished ratings. Keep the identity key separate until both initial sheets are locked.

The **Lead CS Curriculum Adjudicator** records their name on the [adjudication sheet](adjudication_blank.json) and copies both locked initial ratings in full, including notes and abstention reasons. Record the final judgments, flag differences between the initial decisions, and document each disagreement or override of agreement in `resolution_reason`.
