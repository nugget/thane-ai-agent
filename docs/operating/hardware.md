# Hardware Requirements

We're early in Thane's development, so these requirements are based on our
production deployment rather than systematic benchmarking. Consider these
working observations, not final specifications.

## Platform & Portability

Thane is written in Go and designed with macOS integration in mind, but
runs anywhere you can compile Go binaries — which covers the entire POSIX
universe. Our testing focuses on:

- **macOS**: Current releases (we aggressively adopt new features)
- **Linux**: Debian-based distributions (Ubuntu, Debian, derivatives)

Other platforms should work but aren't actively tested. Windows support is
theoretically possible but unexplored.

## What We're Running On

Our primary Thane instance runs on *pocket*, a 2020 M1 MacBook Air with
16 GB RAM. This handles:

- Multiple concurrent conversations across Signal, web, and system channels
- Background loops monitoring 15,000+ Home Assistant entities
- Media analysis workloads (podcast transcription, video processing)
- Continuous operation with ~4-6 hour session lifetimes before memory refresh

The Thane process itself is remarkably lightweight — currently using only
~114 MB of RAM with under 100 MB private memory. The 16 GB recommendation
is about headroom for models and workloads, not Thane itself.

## Model Reality Check

Let's be honest: our production instance relies heavily on Anthropic's API
(Claude) for primary conversations. Local inference on *pocket* is limited
to lightweight routing decisions. When we need serious local model work, we
offload to dedicated GPU hardware.

We believe you could have a good experience with capable open-weight models
on the right hardware, but we haven't systematically tested the full
spectrum yet. The good news: Thane has robust model experimentation tooling
built in. You can swap models, compare outputs, and find what works for
your hardware and quality tradeoff.

See [Your First Thane](guide.md) for model sizing guidance and deployment
options.

## Minimum Viable Setup

For a Thane host (not counting local model inference requirements):

| Resource | Requirement |
|----------|-------------|
| **CPU** | Any modern processor (even modest hardware works) |
| **RAM** | 1 GB for Thane itself; total system needs depend on your model choices |
| **Storage** | 50 GB+ free (conversation archives, media scratch space, model storage) |
| **Network** | Stable connection to Home Assistant and your model providers |
| **OS** | macOS or Debian-based Linux (tested); any POSIX system with Go support (should work) |

For local model inference, requirements vary dramatically by model choice.
A small quantized model might run on 8 GB total system RAM, while larger
models need dedicated GPUs and 32 GB+.

## The Real Message

Thane itself is lightweight — it's the models that drive hardware
requirements. Start with API models on modest hardware, then scale up as
needed. The orchestration layer won't be your bottleneck.

We're learning alongside the community here. As more people run Thane on
different hardware with different models, we'll build better guidance
together. For now: if you can run a Go binary and reach your Home Assistant
instance, you can run Thane. What models you can run locally is a separate
question entirely.
