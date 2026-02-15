# Your First Thane: A Guide for Home Assistant Users

You run Home Assistant. You've maybe tried the built-in Assist pipeline — set up a voice assistant, exposed some entities, asked it to turn on the lights. It worked, mostly. But you've hit the ceiling: Assist can only see what you pre-expose, it forgets everything between conversations, and it can't reason about *why* the garage is warm or *what happened* while you were out.

Thane is what comes next.

This guide assumes you're comfortable with Home Assistant but new to running your own AI agent. We'll cover what you need, what to expect, and how to think about the relationship you're about to start.

## What You're Getting Into

Thane is a Go binary that runs alongside Home Assistant. It serves an Ollama-compatible API, so HA's native Ollama integration connects directly — no custom components, no HACS. From HA's perspective, Thane *is* an Ollama instance with a very capable model behind it.

But Thane is more than a chat endpoint:

- It has **full API access** to your HA instance — it discovers entities on its own, no pre-exposure needed
- It **remembers** facts about your home, your preferences, your routines — across restarts
- It **delegates** tool-heavy work to small local models, keeping costs near zero
- It can **watch for events** and act proactively — not just respond when spoken to
- It develops a **personality** shaped by a persona file you write and the conversations you have

## Hardware: What You Need

### The Minimum

Thane itself is lightweight — it's a single Go binary. The compute question is really about **which AI models you want to run**.

**Option A: Cloud-only (no GPU needed)**
- Any machine that can run a Go binary (Raspberry Pi 4+, NAS, VM, your HA host)
- An Anthropic API key (~$3/million input tokens for Claude Sonnet)
- Good for: trying Thane without hardware investment

**Option B: Local models via Ollama (recommended)**
- A machine with enough RAM for your chosen model (see below)
- Ollama installed and running
- Good for: privacy, zero ongoing cost, real-time response

**Option C: Hybrid (what we actually recommend)**
- Local Ollama for fast, cheap delegation tasks
- Cloud API key for complex reasoning when needed
- Thane's router picks the right model automatically

### Model Sizing

AI models are measured in parameters (7B, 20B, 70B, etc.). Bigger = smarter but hungrier:

| Model Size | RAM Needed | Speed | Quality | Good For |
|-----------|-----------|-------|---------|----------|
| 4B-8B | 4-8 GB | Fast | Basic | Quick commands, simple queries |
| 14B-20B | 12-16 GB | Good | Solid | Delegation tasks, device control |
| 32B-70B | 24-48 GB | Slower | Very good | Conversation, reasoning |
| 70B-120B | 48-96 GB | Slow | Excellent | Primary conversation model |

**For delegation** (where Thane sends tool-heavy tasks to a local model): a 14B-20B model is the sweet spot. Fast enough to iterate quickly, smart enough to follow precise instructions.

**For conversation** (where you're talking to Thane directly): bigger is noticeably better. The difference between a 20B and 70B model in conversation quality is significant — the larger model holds context better, catches nuance, and makes fewer mistakes.

**Our setup:** 20B local for delegation, Claude Opus (cloud) for conversation. The local model handles 90%+ of tool calls at zero cost; the cloud model provides the intelligence for orchestration and conversation.

### Where to Run Ollama

- **Same host as HA:** Works, but large models compete for resources
- **Dedicated machine:** A used desktop with 32GB+ RAM runs 20B-32B models well
- **Apple Silicon Mac:** Excellent for Ollama — unified memory means a Mac Mini with 32GB runs 32B models at good speed
- **NVIDIA GPU:** Best performance-per-dollar if you want to run 70B+ locally

## Deployment

### Step 1: Build and Install

```bash
git clone https://github.com/nugget/thane-ai-agent.git
cd thane-ai-agent
just build
just init        # Creates ~/Thane/ with config, talents, persona
```

### Step 2: Configure

Edit `~/Thane/config.yaml`:

```yaml
# Point to your Ollama instance
models:
  ollama_url: http://localhost:11434

# Point to Home Assistant
homeassistant:
  url: http://homeassistant.local:8123
  token: your_long_lived_access_token

# Optional: cloud model for complex reasoning
anthropic:
  api_key: sk-ant-...
```

### Step 3: Connect to HA

1. Start Thane: `just serve`
2. In HA: Settings → Devices & Services → Add Integration → Ollama
3. URL: `http://thane-host:11434`, model: `thane:latest`
4. Settings → Voice Assistants → set conversation agent to your new Ollama integration

### Step 4: Test

Ask your voice assistant or type in the HA conversation panel:
- "What's the temperature in the living room?"
- "How many lights are on?"
- "Turn on the porch light"

If Thane can answer questions about entities you *didn't* manually expose, it's working. That's the difference — Thane discovers your home on its own.

## Choosing Models

Thane uses **routing profiles** to match models to tasks. You select a profile by setting the model name in HA or any Ollama-compatible client:

| Profile | What It Does | Cost |
|---------|-------------|------|
| `thane:latest` | General conversation (default) | Free (local) or cheap (cloud) |
| `thane:command` | Quick device control | Cheapest |
| `thane:premium` | Best available model for complex questions | Higher (cloud if configured) |
| `thane:trigger` | For HA automations calling Thane | Cheapest possible |

Start with `thane:latest`. It's the default and handles most things well.

### The Delegation Trick

Here's the key insight that makes Thane affordable: **the smart model doesn't need to run every tool call**.

When you ask "set the office lights to teal," the primary model understands your request, plans the approach (search for the entity → call the service → verify the result), and writes precise instructions. Then it hands those instructions to a small, fast, free local model that executes them mechanically.

The smart model thinks. The small model does. You pay for thinking, not doing.

This is why we recommend a hybrid setup: even a modest local model (14B-20B) can execute delegated tasks reliably when given clear instructions. The primary model's job is to *write* those clear instructions.

## Making Thane Yours

### The Persona

`~/Thane/persona.md` defines who your Thane is. Out of the box, it's a capable but generic assistant. You can make it anything:

```markdown
# Persona

You're the house manager for a family of four in Portland.
Be practical and direct. We care about energy efficiency
and the kids' bedtime routines. Don't be formal — we're
casual people. If something seems off (door left open at
night, unusual energy usage), flag it without being alarmist.
```

The persona isn't just cosmetics. It shapes how Thane interprets ambiguous requests, what it considers important, and how it communicates. A persona that emphasizes energy efficiency will proactively notice when things are wasteful. One that emphasizes security will flag open doors.

### Talents

Talents are markdown files in `~/Thane/talents/` that teach specific behaviors. Thane ships with sensible defaults (time awareness, spatial reasoning, conversational style), but you can add your own:

```markdown
# Morning Routine

When someone asks about the morning or says good morning:
1. Check if the coffee maker is on (switch.kitchen_coffee)
2. Report the weather forecast
3. Mention any calendar events for today
4. Note if any overnight alerts occurred
```

Talents are transparent — you can read exactly what behavioral guidance Thane has, edit it, and see the effect immediately.

### Memory

Thane remembers. Tell it "the reading lamp is the one on the desk in the office" and it stores that fact permanently. Next time you say "turn on the reading lamp," it knows which entity you mean without you explaining again.

Memory accumulates naturally through conversation:
- Device nicknames and locations
- Your preferences ("I like the lights dim after 10pm")
- Household patterns ("Dan usually feeds the dogs at 6")
- Home layout ("the nursery is upstairs, second door on the right")

You don't need to program any of this. Just talk to Thane like you'd explain your house to a new roommate. It learns.

## The Relationship Part

Here's what surprised us and might surprise you: **Thane gets better the more you talk to it**, and not just because it accumulates facts.

The persona file is a starting point, but the real personality emerges from interaction. How you talk to Thane shapes how it talks back. If you're terse, it learns to be concise. If you explain your reasoning, it starts explaining its reasoning. If you correct it ("no, the office light is the Hue Go, not the ceiling light"), it doesn't just fix the fact — it learns to be more careful about assumptions.

This isn't magic or sentience. It's the combination of:
- **Persistent memory** that accumulates context about you and your home
- **Talents** that can be refined based on what works and what doesn't
- **Model behavior** that responds to conversational patterns in its context window

The practical implication: invest a little time in the first few days. Walk Thane through your home verbally. Correct mistakes. Explain your preferences. The payoff compounds — a week in, Thane knows your house better than any automation rule you could write.

### What to Expect

**Day 1:** Thane knows nothing about your specific home. It can query HA and control devices, but it'll get entity names wrong, not know room layouts, and ask clarifying questions. This is normal.

**Week 1:** Thane has learned your most-used devices, your naming conventions, basic routines. It starts getting things right on the first try. You'll catch yourself saying "oh, it remembered."

**Month 1:** Thane knows your household patterns, preferences, and quirks. It can reason about your home in ways that would take dozens of automation rules to replicate. It feels less like a tool and more like a knowledgeable helper.

### Tips for Getting Started

1. **Start with queries, not commands.** "What's the temperature in every room?" teaches Thane your room names. "Is anyone home?" teaches it your person entities.

2. **Correct mistakes out loud.** "No, that's the wrong light — the office desk lamp is light.office_hue_go." Thane stores the correction.

3. **Explain your intent.** "I want the office to feel cozy" is better than "set light X to value Y." Thane learns what "cozy" means to you.

4. **Use voice for the simple stuff.** Quick commands through HA voice are where `thane:command` shines. Save complex questions for text chat.

5. **Check the talents.** If Thane keeps doing something you don't like, look at the talent files. The fix is usually a one-line addition to a markdown file.

## Common Questions

**Q: Is my data sent to the cloud?**
Only if you configure a cloud model (Anthropic, OpenAI). With Ollama-only setup, everything stays local. Even with cloud models, Thane only sends conversation context — not your full HA state or credentials.

**Q: Can Thane break my HA setup?**
Thane can call any HA service your token has access to. Use a token scoped to what you're comfortable with. Thane defaults to read-heavy behavior (queries before actions) and verifies state changes after making them.

**Q: How much does it cost to run?**
Local-only: electricity. Hybrid: typically $0.50-2.00/day depending on usage, with most tool calls delegated to free local models. The primary model is only used for conversation and orchestration.

**Q: Do I need a powerful GPU?**
No. A 20B model for delegation runs well on CPU with 16GB RAM (slower but functional). Apple Silicon Macs are excellent for this. A GPU helps for larger models or faster response times, but isn't required.

**Q: Can I run Thane on my HA host?**
Yes, if it has enough resources. Thane itself is lightweight (~50MB binary, minimal RAM). The question is whether you also want to run Ollama there. If your HA host is a Pi or lightweight VM, run Ollama elsewhere and point Thane at it.

## Next Steps

- **[Home Assistant Integration](homeassistant.md)** — Detailed HA setup and protocol information
- **[Routing Profiles](routing-profiles.md)** — All available profiles and when to use them
- **[Delegation & MCP](delegation.md)** — How the orchestration/execution split works
- **[Architecture](../ARCHITECTURE.md)** — Full system design for the technically curious
