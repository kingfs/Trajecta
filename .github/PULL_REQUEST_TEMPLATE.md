## Description
<!-- Provide a brief description of the changes in this PR -->

## Linked Issues
<!-- Link to any related issues (e.g., Closes #123) -->

## Type of Change
- [ ] Bug fix (non-breaking change which fixes an issue)
- [ ] New feature (non-breaking change which adds functionality)
- [ ] Breaking change (fix or feature that would cause existing functionality to not work as expected)
- [ ] Documentation update
- [ ] Refactoring

## Verification Performed
<!-- Describe the tests that you ran to verify your changes. Provide instructions so we can reproduce. -->
- [ ] `task check:quick` (always; formatting, lint, short tests)
- [ ] `task check:full` (if you touched storage, the record format, the proxy, the Responses runtime, routing, or the UI)
- [ ] `task ui:build` (if you changed the monitor UI; commit the rebuilt `internal/monitor/ui/dist` in the same change)
### Screenshots (if applicable)
<!-- Add screenshots or screen recordings if relevant -->

## Checklist
- [ ] My code follows the style guidelines of this project
- [ ] I have performed a self-review of my own code
- [ ] I have commented my code, particularly in hard-to-understand areas
- [ ] I have made corresponding changes to the documentation (including `CHANGELOG.md` when user-visible)
- [ ] My changes generate no new warnings
- [ ] I have added tests that prove my fix is effective or that my feature works
- [ ] New and existing unit tests pass locally with my changes
- [ ] Record format replay compatibility is preserved (`pkg/replay` is a hard requirement; see CONTRIBUTING.md)
