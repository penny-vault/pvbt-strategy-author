# Changelog

All notable changes to this plugin are recorded here. This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## 0.1.0 - 2026-04-07

Initial release. This plugin ships with two tools and a curated knowledge base derived from pvbt 0.6.0.

- The `pvbt-strategy-design` skill extracts strategy intent from natural-language descriptions during brainstorming, fills sensible defaults for every slot in the pvbt strategy schema, and pauses only for genuine ambiguity or flagged risk.
- The `pvbt-strategy-reviewer` agent reviews strategy code in three passes: correctness, pvbt idiom, and quant red flags such as survivorship bias, lookahead bias, leaked state, insufficient warmup, and over-parameterization. Each finding cites the exact reference section that explains the principle.
- Nine curated reference documents ship inside the plugin and are loaded on demand by the agent and skill, so authors do not need a pvbt checkout at runtime.
