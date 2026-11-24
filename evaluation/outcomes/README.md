# Unaided learning outcome evaluation

Use this protocol for a voluntary study run by a teaching partner. It asks whether presenting equivalent material in graph prerequisite order improves unaided transfer. Treat stated modality as a presentation preference. Measure starting performance with the baseline assessment.

## Before recruitment

- The teaching partner approves the study before enrollment. Its study lead obtains institutional approval and preregisters the protocol with the project owner. Specify the course, eligible population, baseline strata, assessment forms and schedule, scoring rubric, minimum meaningful effect, sample size assumptions, recruitment target and deadline, missing-score shifts and decision rule. Record any amendments and when they were made.
- The partner's consent lead provides accessible consent and withdrawal instructions, explains what is collected and how results will be shared, and verifies consent before baseline. Obtain guardian consent and learner assent where applicable. Declining or withdrawing has no effect on course access or grades. Explain which records the partner will remove on withdrawal and which aggregate counts it may retain.
- The partner's accessibility lead checks both arms' materials and all forms before enrollment, including keyboard and screen-reader access and approved accommodations. Offer equivalent access in both arms. Apply accommodations that preserve independent work consistently across assessments.
- The partner's data steward restricts access to consent records, assignments, submissions, scores and the separately stored linkage key. Keep records at the partner, outside the repository, service requests, prompts, traces and logs. Honor withdrawals and delete identifiable records, including the linkage key and identifiable backups, within 90 days of final analysis.
- The partner's study lead publishes aggregate participant flow, effect estimates, uncertainty and sensitivity results, including adverse findings and any early stop. Apply the partner's disclosure rules when sharing counts. Follow the participant flow and outcome reporting guidance in the [CONSORT 2025 statement](https://www.bmj.com/content/389/bmj-2024-081123).

## Allocation and assessments

After consent and baseline, assign learners individually in a 1:1 ratio within prespecified baseline score strata. A partner allocator uses a concealed random sequence of balanced, randomly permuted blocks for each stratum and releases the next assignment only after the learner is enrolled and their baseline is recorded. Keep upcoming assignments from recruiters and assessors. Report learners in the arm assigned to them, including those who stop using the material.

The graph arm receives the course materials in graph prerequisite order. The control receives the same materials in a fixed conventional order set before assignment. Match objectives, examples, exercises, available assistance, instruction time and allowed study time. Record attendance, departures from the assigned material and exposure to the other sequence.

| Assessment | When | What to score |
| --- | --- | --- |
| Unaided baseline | Before assignment | Starting skill on a parallel form. Use the score for strata and baseline adjustment. |
| Unaided post-test | Immediately after instruction | Performance on a distinct parallel form. Report as a secondary outcome. |
| Unaided transfer | 14 days after instruction | Application of the same objectives to previously unseen tasks. This is the primary outcome, scored from 0 to 100. |

Use the same forms across arms at each time point. Define the item blueprint, rubric and 0–100 scoring before allocation. Check comparable coverage and difficulty across forms. Proctor assessments under the same conditions, with access accommodations and without instructional materials, hints, solutions or outside help. Separate instruction from unaided assessment to measure independent performance. [Classroom randomized experiment with separate practice and unaided tests](https://pmc.ncbi.nlm.nih.gov/articles/PMC12232635/)

Partner scorers use coded submissions and remain blind to assigned order. Calibrate with common scored examples, double-score a prespecified subset and resolve disagreements under the same blind. Lock scores before joining them to assignments.

## Analysis and decision

The primary estimand is the difference in mean 14-day unaided transfer scores under assignment to graph order versus conventional order among all randomized learners, regardless of adherence. Fit a linear regression of transfer score on assigned arm and the baseline score. Use the arm coefficient for the adjusted difference, with a common baseline slope and residual variance. A common constant baseline reduces the fit to an arm comparison. If baseline and arm are perfectly confounded, the adjusted difference is unavailable. Report the immediate post-test separately.

Report the numbers assigned and scored, with missing rates based on everyone assigned. Keep recorded zeros distinct from missing scores. Report reasons for missingness and compare baseline summaries for learners with and without transfer scores. Follow up equally in both arms and retain permitted aggregate counts of withdrawals.

For missing scores, assume missingness is independent of the unobserved outcome conditional on assigned arm and baseline score. Use 200 multiple imputations with an arm-specific normal regression of observed transfer score on baseline. For each draw, sample residual variance from the residual sum of squares divided by a chi-squared draw at the residual degrees of freedom. Draw the fitted mean at the arm's observed average baseline and, when estimable, the baseline slope using their conditional normal standard errors. Add a residual draw for each missing score, and limit predictions to the 0–100 scale. Freeze the coded row order and random seeds before analysis. Use an arm mean without a slope only if all assigned baselines in that arm are the same. If a missing arm lacks data to fit its imputation, report the primary result as unavailable. A fit with zero residual variance uses the fitted values.

For each completed dataset, refit the baseline-adjusted arm comparison. Pool the coefficients by averaging them. Compute the total variance as the mean of the within-imputation variances plus `(1 + 1/m)` times the sample variance between imputation coefficients, with `m = 200`. Form the two-sided 95% normal-approximation interval as the pooled difference plus or minus `z(0.975)` times the square root of this total. With complete data, use the single fit and its variance.

Accumulate each arm's baseline and transfer moments using offsets from its first observed baseline and transfer scores. In both regressions, treat residual sums of squares at or below `8 × n × ε × max(total, explained)` as zero. Here `n` is the number of rows in the fit, `ε = 2⁻⁵²` is machine epsilon for double precision, and `total` and `explained` are the corresponding sums of squares. Report an estimable difference when its interval is unavailable, such as when there is zero estimated variance.

Repeat imputation analysis with the same draws at adverse shifts of 2.5 and 5 points. Subtract each shift from imputed graph scores and add it to imputed control scores, then limit to 0–100 again. Keep observed scores fixed. Report the difference and interval for the primary and both shifted results. Identify the first tested shift at which the lower endpoint fails to exceed the margin. With complete data the shifts have no effect.

Declare improvement only if the unrounded lower endpoint is **strictly greater** than the agreed margin in the primary and every applicable sensitivity interval, recruitment reaches the target in both arms, missing scores stay at or below the per-arm limit, and the absolute missing-rate gap stays at or below its limit. In the worked example, the margin is 5 points, the recruitment target is 168 assignments per arm, the missing-rate limit is 15% per arm, and the gap limit is 10 percentage points. A failed condition or unavailable interval makes the improvement result inconclusive. Report estimates and intervals when available, including adverse findings.

Close recruitment at its target or at the preregistered deadline. Analyze once after the assessment and score lock. Stop the study for a consent or privacy failure and classify the stopped run as inconclusive. Publish the reason and any permitted aggregate results. Use the closing aggregate analysis of a stopped run as its final analysis for the deletion deadline. Avoid early stopping for apparent benefit.

## Synthetic worked example

These values are illustrative assumptions agreed with the project owner. The owner and teaching partner must confirm and freeze the study parameters before recruiting.

Use a 5-point meaningful margin, a 10-point planning effect and a common within-arm standard deviation of 15 points. Target 80% power for the lower endpoint of the two-sided 95% interval to exceed 5 points under equal allocation. With `z(p)` the standard normal quantile at probability `p`, the scored target per arm is

```text
ceil(2 × 15² × (z(0.975) + z(0.80))² / (10 − 5)²)
= ceil(141.2798)
= 142
```

Allowing for 15% loss gives `ceil(142 / 0.85) = 168` assignments per arm, or 336 total. This approximation assumes independent observations and the stated variance. It estimates power to clear the primary interval threshold at the scored target. The recruitment, attrition and sensitivity rules also apply.

Each arm in the first fixture has 84 baseline scores of 40 and 84 of 70. Transfer equals baseline plus alternating residuals of −2 or +2, with an added 10 points in graph. Remove 16 high-baseline graph scores and 16 low-baseline control scores. In the tipping-point fixture, both arms start with a constant baseline of 50. Transfer alternates 59.5 and 61.5 in graph and 54 and 56 in control. Remove 16 scores per arm, evenly from both values. Both fixtures have 152 of 168 scores per arm, or 9.52% missing in each arm.

| Synthetic case | Shift per arm (points) | Difference (points) | 95% CI (points) | Final conclusion |
| --- | ---: | ---: | --- | --- |
| Baseline-dependent dropout | 0 | +10.00 | 9.55 to 10.46 | Improvement |
| Baseline-dependent dropout | 2.5 | +9.52 | 9.05 to 10.00 | Improvement |
| Baseline-dependent dropout | 5 | +9.05 | 8.50 to 9.59 | Improvement |
| Tipping point | 0 | +5.50 | 5.27 to 5.73 | Inconclusive across shifts |
| Tipping point | 2.5 | +5.02 | 4.75 to 5.30 | Inconclusive across shifts |
| Tipping point | 5 | +4.55 | 4.16 to 4.93 | Inconclusive across shifts |

The tipping-point fixture meets recruitment and attrition limits. The first shift at which it fails the interval rule is 2.5 points. The conclusions apply to the generated rows.

From the repository root, run the calculation, worked example and synthetic tests with:

```sh
go test ./evaluation/outcomes/...
```
