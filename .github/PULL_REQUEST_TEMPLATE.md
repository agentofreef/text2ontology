<!-- Thanks for opening a PR. Please fill out the sections below. -->

## Summary

<!-- One or two sentences: what does this PR do and why? -->

## Type of Change

- [ ] Bug fix (non-breaking change which fixes an issue)
- [ ] New feature (non-breaking change which adds functionality)
- [ ] Breaking change (fix or feature that would cause existing functionality to not work as expected)
- [ ] Documentation update
- [ ] Refactor / no functional change

## Related Issue

<!-- Link to any related issue, e.g. "Closes #123" -->

## Testing

<!-- Describe the tests you ran. If you couldn't test locally, say so. -->

- [ ] `go vet ./...` passes for affected services
- [ ] `go test ./...` passes for affected services (or skips cleanly without DATABASE_URL)
- [ ] Frontend `npx tsc --noEmit` passes (if frontend touched)
- [ ] `scripts/check-layer-deps.sh` passes (if Go services touched)

## Sync Cadence Notice

This community repo receives one-way syncs from a private dev repo on a roughly monthly cadence. Your PR will be reviewed and, if accepted, ported into the private repo, then surfaces back here in the next sync. See [CONTRIBUTING.md](../CONTRIBUTING.md) for details.

## Checklist

- [ ] My code follows the style conventions of this project
- [ ] I have updated documentation where needed
- [ ] My changes generate no new warnings
- [ ] I have signed off my commits (if my jurisdiction requires it)
