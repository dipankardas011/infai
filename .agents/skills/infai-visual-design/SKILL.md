---
name: infai-visual-design
description: infai visual identity and design artist prompt generator for product UI, terminal screenshots, launch graphics, social posts, diagrams, and other branded visuals
---

# infai Visual Design System

Load this skill when creating, reviewing, or prompting artwork for infai. Use it for
the marketing site, terminal UI screenshots, release graphics, product diagrams,
social assets, and image-generation prompts. The goal is to make every visual feel
like infai: a focused local inference control plane, not a generic AI product.

## Source Of Truth

The implemented product is authoritative:

- Marketing composition and copy: `docs/index.html`
- Marketing tokens, typography, effects, and layout: `docs/style.css`
- Terminal palette and theme behavior: `tui/themes.go`
- Terminal component language: `tui/styles.go`, `tui/header.go`
- Product screenshots: `docs/profile-config.webp`, `docs/runningmodel.webp`

Inspect these files before inventing a new visual direction. The default public
identity is based on the Everforest-like dark palette in `docs/style.css`; the TUI
also supports alternate themes, but external branded artwork should use the
canonical palette below unless a specific theme is requested.

## Brand Character

infai should feel:

- Focused, self-hosted, and technically capable
- Calm under operational complexity
- Precise, inspectable, and honest about system state
- Slightly editorial and design-conscious, without losing terminal credibility
- Built for people running models on their own hardware

Avoid:

- Generic blue-purple AI gradients
- Glossy SaaS dashboards or futuristic holograms
- Robot heads, glowing brains, humanoid assistants, or stock people
- Cloud imagery, data-center fantasy, or implied hosted infrastructure
- Excessive neon, cyberpunk clutter, or hacker clichés
- Fake benchmarks, invented telemetry, unreadable dashboard soup, or unsupported claims
- Light pastel UI unless the request explicitly asks for a non-canonical theme

## Canonical Palette

Use these exact values for external artwork and image-generation prompts. In frontend
code, use the existing CSS variables or semantic theme tokens instead of duplicating
hex values.

| Role | Value | Use |
| --- | --- | --- |
| Deep background | `#0c1114` | Header strips, deepest voids, contrast areas |
| Background | `#11171a` | Main canvas and terminal atmosphere |
| Surface | `#1a2226` | Cards, panels, terminal bodies |
| Raised surface | `#212b30` | Panel chrome, active subdivisions |
| Border | `#2c383e` | Quiet separators and panel edges |
| Highlight border | `#3d4d54` | Focused outlines and controls |
| Primary text | `#d3c6aa` | Warm cream headings and key values |
| Secondary text | `#9aa79d` | Supporting copy and descriptions |
| Muted text | `#66736e` | Metadata, hints, inactive controls |
| Signal green | `#a7c080` | Primary action, healthy/running state, accent |
| Aqua | `#83c092` | Throughput, telemetry, secondary positive data |
| Yellow | `#dbbc7f` | Warnings, constrained resources, attention |
| Pink | `#d3869b` | Secondary accent and contrast signal |
| Error red | `#e67e80` | Failure, destructive action, stopped/error state |

Default balance: `#11171a` dominates, `#d3c6aa` carries readable content, and
`#a7c080` is used sparingly as a meaningful operational signal. Do not turn every
surface green.

## Typography And Texture

- Display and editorial headlines: Bricolage Grotesque, heavy weight, tight tracking,
  short line lengths, strong contrast between lines.
- Technical values, labels, commands, and status: Martian Mono or a comparable
  compact monospace.
- Use at most two type families in one asset.
- Use uppercase mono kickers with generous letter spacing for system labels such as
  `[ local inference control plane ]` or `[ 03 / the glass ]`.
- Favor thin rules, compact metadata, tabular values, and clear whitespace.
- Use a faint square grid, subtle grain/noise, restrained radial green glow, and
  soft translucent navigation surfaces when appropriate.
- Rounded corners should be modest, usually 6–12px. Prefer thin borders over heavy
  glassmorphism and avoid excessive drop shadows.

## Visual Motifs

Use one or two motifs to explain the product, not all of them at once:

- A terminal control surface with tabs such as Profiles, Runs, Models, and Engines
- A profile equation: `profile = model + engine + config`
- Flag soup collapsing into one clean saved profile
- Several local model processes flowing into one monitoring pane
- Compact status dots, uptime, ports, tokens-per-second, CPU/RAM/VRAM meters
- A single Go binary, SQLite file, or local machine boundary as a quiet anchor
- Thin grid lines, horizontal rules, bracket labels, arrows, and small diamonds
- A restrained green glow around a healthy running process

Show local hardware as the source of control: a workstation, terminal, or abstract
machine boundary. Never imply that infai sends user data to a remote cloud.

## Composition Rules

- Establish one clear focal point and one message.
- Prefer an asymmetrical editorial layout: copy or a short system label on one side,
  a terminal panel, process diagram, or data-flow object on the other.
- Keep the primary object large and legible; leave dark negative space around it.
- Use a grid or ruled structure to create order, then one green signal to create focus.
- For posters, use a short headline, one explanatory line, and a small infai/status
  lockup. Do not fill the poster with feature lists.
- For diagrams, use a left-to-right flow and label only real concepts: model, profile,
  engine, config, process, logs, telemetry, port.
- For screenshots, use real repository assets or real UI output whenever possible.
  Redact local paths, usernames, private hostnames, tokens, and proprietary model
  names before publication.

## Design Artist Prompt Generator

When the user asks for a visual prompt, first identify these inputs when available:

1. Asset type: UI concept, screenshot treatment, poster, social card, diagram, or icon
2. Subject: the infai feature or message being shown
3. Audience and destination: developer, release note, website, X/LinkedIn, or docs
4. Canvas: aspect ratio, dimensions, and whether text must be exact
5. Reference: a supplied screenshot, logo, or existing page section

If inputs are missing, make reasonable defaults and state them briefly. Return a
production-ready prompt, then a short negative prompt and a text/layout note. Use
this template as the base and adapt the subject:

```text
Create a polished infai product visual for [ASSET TYPE] about [SUBJECT].
Use the attached infai interface or screenshot as the primary style reference.

Visual language: dark local-operations control plane, editorial terminal design,
asymmetrical composition, generous negative space, thin ruled borders, compact
monospace metadata, and one clearly selected healthy process. Use a deep charcoal
canvas (#11171a / #0c1114), raised panels (#1a2226 / #212b30), warm cream text
(`#d3c6aa`), muted gray-green support text (`#9aa79d`, `#66736e`), signal green
(`#a7c080`) as the primary accent, aqua (`#83c092`) for throughput, yellow
(`#dbbc7f`) for warnings, pink (`#d3869b`) for secondary contrast, and red
(`#e67e80`) only for errors.

Show [SPECIFIC VISUAL MOTIF]. Include only these factual labels or concepts:
[EXACT TEXT OR DATA]. Make the hierarchy clear: [HEADLINE], [SUPPORTING LINE],
and [SMALL STATUS/BRAND LOCKUP]. Keep the product local, self-hosted, and
inspectable; no cloud dependency or remote data-center imagery.

Finish as [CANVAS / ASPECT RATIO], with crisp edges, accessible contrast, subtle
grain/noise, restrained green glow, and enough empty space for final typography.
```

Negative prompt:

```text
generic SaaS landing page, blue-purple gradient, glossy glassmorphism, neon
cyberpunk, robot, glowing brain, humanoid AI, stock people, cloud data center,
floating holograms, fake metrics, unreadable microcopy, excessive rounded cards,
busy dashboard collage, oversaturated colors, white background, invented logo,
watermark, misspelled text
```

For image models that render text poorly, instruct them to create the composition
with blank label areas and add exact copy afterward in HTML, SVG, or a design tool.
Never let generated artwork invent version numbers, performance claims, model names,
or telemetry values.

## Prompt Variants

Use these focused directions when useful:

- **Release poster:** dark grid, one strong claim, terminal/process motif, green
  running indicator, small `infai` lockup, no feature wall.
- **Feature diagram:** cream text on charcoal, profile equation or flag-soup-to-profile
  transformation, arrow flow, exact labels, no decorative architecture.
- **Social card:** high contrast at small size, one headline under eight words, one
  large terminal panel or green signal, generous margins, no tiny dashboard copy.
- **Screenshot treatment:** preserve actual UI geometry and data, add only a restrained
  frame, caption, or callout; do not redesign the screenshot into a fake dashboard.
- **Icon or mark:** geometric `▸`/cursor energy, compact terminal silhouette, green
  signal on charcoal, simple enough to read at favicon size; do not add an AI brain.

## Review Checklist

- Does this look like infai rather than a generic AI or SaaS product?
- Is the dark charcoal and warm cream foundation intact?
- Is signal green used as a meaningful operational state rather than decoration?
- Is there one clear message and one visual metaphor?
- Are model, engine, profile, process, port, logs, and telemetry represented accurately?
- Are all metrics, names, and claims real or clearly marked as illustrative?
- Are exact words reserved for final typesetting when image generation may corrupt them?
- Are private paths, hostnames, usernames, tokens, and model identifiers removed?
- Does the asset remain legible at its intended size and work with reduced motion if animated?
