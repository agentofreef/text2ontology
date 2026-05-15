# Contributing to text2ontology

Thank you for considering a contribution. Please read this in full before opening a PR.

## How this project is developed

This repo is where I actually develop. No private mirror, no scheduled sync — commits land here directly. A few things worth knowing up front:

- I read PRs and Issues as I see them. Response time varies (this is something I do alongside a day job), but nothing is being held back in a private queue.
- Direct pushes to `main` do happen. The GitHub Actions release workflow is the main safety net right now, calibrated to one person working at this pace. If the project grows enough contributors that pushes-to-main start causing friction, the protections will tighten — that's a happy problem to have when it shows up.

---

## What kinds of contributions help

### High value
- **Bug reports with reproducible cases** (especially recall accuracy issues)
- **Documentation improvements**, especially translations or English-language polish
- **New language i18n** for the frontend
- **Reference architecture diagrams** that clarify our design

### Medium value
- **Tests** for existing services (we have CI but coverage isn't complete)
- **Performance benchmarks** for SmartQuery / Recall
- **Examples** of ontologies for different industries

### Please discuss first (open an Issue)
- New core concepts (e.g., proposed additions to OD / OK / OL / Intent)
- Schema changes
- New top-level services
- Anything that touches the public ABI of `pkg/`

We're happy to accept these but want to align direction before you invest time.

---

## Contribution flow

### Reporting Issues

Use the GitHub Issues tab. Choose the right template:

- **Bug**: something doesn't work as documented
- **Feature**: a proposal for new functionality
- **Question**: how do I X? (also see Discussions)

Include version info, steps to reproduce, expected vs actual behavior.

### Submitting Pull Requests

1. **Fork the repo** on GitHub
2. **Create a branch** from `main`: `git checkout -b fix/recall-fuzzy-collapse`
3. **Make changes**, keeping commits focused and atomic
4. **Run tests locally**:
   ```bash
   # Per-service tests
   for svc in backend-api agent-server recall-server lakehouse-sql-server mcp-tools-server collector-server; do
     (cd services/$svc && go test ./...)
   done

   # Frontend
   cd frontend && npm run lint && npm run build
   ```
5. **Sign off your commits** if your jurisdiction requires it
6. **Open a PR** against `main` with:
   - A clear title following Conventional Commits style (`fix:`, `feat:`, `docs:`, etc.)
   - A description explaining *why* (not just *what*) the change exists
   - Link to any related Issues

### Style

- **Go**: follow standard `gofmt` + `goimports`. We use `go vet` in CI
- **TypeScript**: project uses strict mode + ESLint
- **Markdown docs**: use clear hierarchy (1-3 levels max), tables for comparisons, concrete examples over abstract description
- **Comments**: explain *why*, not *what*. Default to no comments

---

## Code of Conduct

This project follows the [Contributor Covenant 2.1](./CODE_OF_CONDUCT.md). By participating, you agree to its terms.

---

## License

By submitting a PR you agree to license your contribution under the project's licenses:
- Code: Apache 2.0
- Documentation: CC BY 4.0

You retain copyright but grant a perpetual, irrevocable license for use within the project.

---

## Questions?

- For project direction and design questions: open a Discussion
- For implementation details: open an Issue with the `question` label
- For commercial inquiries: reach out via the contact at [text2ontology.com](https://text2ontology.com)
