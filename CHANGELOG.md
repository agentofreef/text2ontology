# Changelog

All notable changes to text2ontology will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Initial public release of text2ontology
- Seven-service Postgres-only architecture (backend-api, agent-server, recall-server, lakehouse-sql-server, mcp-tools-server, collector-server, plus frontend)
- Three Agent modes: lakehouse (query), builder (modeling), analyst (governance)
- Five L3 validators for AI-driven ontology changes
- Metric Intent system with auto-pivot
- Thread Memory Ledger for cross-turn structured memory
- Per-step agent logging (`agent_step`)
- Full documentation: manifesto, design philosophy, BOE role, commercial thesis

### Known Limitations
- OL → OK auto-sedimentation: design intent only, not yet implemented
- Ontology version control: agent decisions are logged, but ontology-itself history is not yet recorded
- English translations for several long-form essays still pending

[Unreleased]: https://github.com/agentofreef/text2ontology
