# Model Evaluation

`model-eval` captures the exact provider-neutral inputs and reference decision
for each model call in a production request, then replays those calls against an
OpenAI-compatible deployment without executing any tools. It is intended for
comparing local models against production workloads rather than generic
benchmark prompts.

## Safety boundary

Snapshots contain production system prompts, conversation history, tool
results, and schemas. They are private operational data, not repository
fixtures. The command:

- writes snapshots and reports with mode `0600`
- defaults to `~/Thane/evals`, outside the source tree
- refuses to write an artifact inside the current Git worktree
- accepts API credentials only through a named environment variable
- never executes a captured tool

Retention limits are not redaction. Do not commit or publish snapshots even if
they look harmless after casual inspection.

## Enable capture

Per-iteration capture follows the existing retained-content policy. Enable it
in the production configuration:

```yaml
logging:
  retain_content: true
  # Increase this if production tool results exceed the normal 4096-character
  # forensic-retention ceiling. Truncated calls are never exported.
  max_content_length: 65536
```

Restart Thane after changing the setting. Requests recorded before a build with
model-call capture support do not contain the exact per-iteration tool surface
and cannot be exported as evaluation cases.

Each captured call contains:

- the exact message array supplied to the model
- the tool schemas visible on that iteration
- semantic system-prompt section boundaries
- the model response before any tool executes
- provider stop reason, token counts, and latency

Large message bodies still follow `logging.max_content_length`; choose a bound
large enough for the contexts under evaluation while accounting for the added
private storage. The exporter skips any case whose input was truncated, as well
as image-bearing cases whose media bytes were intentionally not retained.

## Capture a snapshot

Capture up to 100 requests from the last seven days:

```sh
just model-eval snapshot
```

Common selection controls:

```sh
just model-eval snapshot --since 24h --limit 50
just model-eval snapshot --model spark/gpt-oss:120b
just model-eval snapshot --request-id r_123 --request-id r_456
```

The command reports how many cases were skipped because they were truncated,
contained images, exhausted, or lacked model-call capture. Exhausted requests
are excluded by default; use `--include-exhausted` when failure recovery is the
behavior under study.

## Replay one model

Point the runner at any server implementing OpenAI chat completions. Streaming
is on by default so the same tool-call accumulation and termination checks used
in production are exercised.

```sh
just model-eval run \
  --snapshot ~/Thane/evals/snapshot-20260820T180000Z.thane-eval.json \
  --base-url http://spark:8000/v1 \
  --model Qwen3.5-122B-A10B
```

If the endpoint requires authentication, name the environment variable rather
than putting a token on the command line:

```sh
just model-eval run \
  --snapshot ~/Thane/evals/snapshot-20260820T180000Z.thane-eval.json \
  --base-url https://runner.example/v1 \
  --model local-model \
  --api-key-env LOCAL_MODEL_API_KEY
```

`--tool-contract auto` is the default. It replaces only the captured
model-family tool-calling contract using Thane's current interaction profile
for the target model; the rest of the production prompt remains unchanged.
Use `--tool-contract exact` to test a literal drop-in replacement, or `native`
and `raw-text` to compare those two contracts deliberately. `--provider`,
`--family`, and `--trained-for-tool-use` supply the same profile hints used by
the runtime catalog.

## Reading a report

The report separates transport reliability from semantic agreement:

- `responses` versus `errors` shows whether the deployment completed the call
- `decision_matches` compares text versus tool-call decisions
- `tool_name_matches` ignores parallel-call ordering
- `tool_argument_matches` compares names and semantic JSON arguments while
  ignoring provider-assigned correlation IDs
- `reference_matches` is exact deterministic agreement for tool decisions and
  decision-kind agreement for text responses

Reference agreement is not a quality oracle. Production can choose a merely
acceptable path, and two correct plans can use different tools. Text response
quality is intentionally left ungraded. Use the deterministic report to find
divergent cases, then curate those cases or add a separate judge before making
a production routing decision.
