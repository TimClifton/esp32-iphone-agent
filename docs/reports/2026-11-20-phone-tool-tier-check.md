# Small phone-tool model routing check

Scope: one offline task per assigned tier, using two saved neutral iPhone Search screenshots. No live phone operation, app journey, broader benchmark, or paid inference API was added. The screenshots were inspected but are not committed.

| Assigned worker | Observed result | Image access and retries |
| --- | --- | --- |
| Existing lightweight Luna worker | Correctly identified empty Search versus exact `CPTest42`; correctly located clear button near (660,989). It volunteered the wrong image height (1536 instead of 1560), so dimensions must come from the capture metadata, not model estimates. | Actually viewed both images; no reported visual retries. |
| Explicit `gpt-6.1-terra` worker | Correctly identified both states and text. Proposed clear button center (660,990), bounds x644–676/y974–1006, and the correct empty-field after-condition. | Two image-view calls in one tool invocation, zero retries. No tap performed. |

Actual provider model identifiers, token billing, model-call accounting and per-tier prices were not exposed by these workers. Dispatch assignment is evidence of the configured tier, not independently verified provider identity. This test does not establish the absolute cheapest model or a measured monetary saving. Model rounds must not be confused with image tool-call counts.

## Practical default

- Run known connection/readiness/recovery/release mechanics and fixed action sequences as scripts. Those scripts make no model calls; an agent is only needed for a decision or interpreting the evidence.
- Use the configured Luna tier for simple known-screen, empty/filled-field and exact short-text checks. It demonstrated useful image understanding here. Obtain image dimensions from tool metadata and validate any proposed coordinates against bounds.
- Use Terra when the layout is unfamiliar, a target is ambiguous, or Luna cannot reliably establish the state or target. Terra demonstrated a correct bounded navigation proposal in this test; it was not given independent live control.
- Escalate beyond those tiers only for a demonstrated unresolved diagnosis or a review requiring stronger reasoning. Do not route routine taps, captures, retries or fixed tests to a premium model by default.

This is a routing recommendation based on a deliberately small capability sample, not a general visual-accuracy certification. Revalidate unfamiliar screens and fail closed on uncertain targets. The generic phone-process acceptance and CP migration conditions remain separate.
