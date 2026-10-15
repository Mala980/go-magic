---
name: code-review
description: "Use when reviewing a pull request, diff, or merge request, when establishing code review standards and checklists for a team, or when giving feedback on someone else's code. Also use when triaging review comments that feel contentious or when a PR is too large to review well."
version: 2.0.0
author: go-magic
license: MIT
tags: [code-review, quality, collaboration, guidelines]
tools: [read_file, search_in_files]
---

# Code Review Skill

Guidelines for conducting code reviews that catch issues early, spread knowledge, and keep the codebase consistent.

## When to Use

Load this skill when:
- Reviewing a pull request, merge request, or a working diff
- Establishing review standards or a checklist for a team
- Giving structured feedback on code you did not write
- Deciding how to raise a concern without making it personal
- A PR is too large or the discussion is going in circles

## Quick Reference

### Review Checklist
```
✓ Security vulnerabilities checked
✓ Error handling comprehensive
✓ Code follows project conventions
✓ Performance implications considered
✓ Test coverage adequate
✓ Documentation complete
✓ No sensitive data exposed
✓ Breaking changes documented
```

### Comment Format
```
[Priority] Type: Description

Examples:
[High] Security: SQL injection vulnerability in user input
[Medium] Performance: N+1 query in user listing
[Low] Style: Consider extracting to helper function
```

Priority reflects impact, not certainty: `High` = must fix before merge, `Medium` = should fix, `Low` = nice to have. Review in that order — security and correctness before style.

## Review Process

### 1. Understand the Context
- Read the PR description and any linked issue; the "why" is often more reviewable than the "what"
- Check whether tests are included
- Look at the diff stat (files and lines changed) to calibrate effort
- If the PR mixes unrelated changes, ask for a split before doing a line-by-line pass

### 2. Line-by-Line Review
- Go from high-level structure down to details
- Focus on logic, not formatting — let linters and formatters own style
- Verify edge cases: empty input, maximum sizes, concurrent access, failure paths
- Check error handling for silent failures and leaked internals

### 3. Provide Constructive Feedback
- Be specific and actionable; point at a line, not at a person
- Explain "why", and suggest a concrete alternative when rejecting an approach
- Use "we" instead of "you" — the code is shared, and so is the outcome
- Approve early when the code is good; do not hold the PR hostage over nits

## What to Check

### Security
- [ ] Input validation and sanitization
- [ ] Authentication and authorization checks on every new entry point
- [ ] SQL injection prevention (parameterized queries, never string concatenation)
- [ ] XSS prevention (output encoding at the boundary)
- [ ] No hardcoded secrets, tokens, or credentials
- [ ] Dependency vulnerabilities (`go list -m -u all`, `npm audit`, `pip check`)

### Code Quality
- [ ] Clear, descriptive naming
- [ ] Single responsibility per function
- [ ] No duplicated logic
- [ ] Abstraction level is appropriate (not over-engineered, not copy-pasted)
- [ ] Follows project conventions
- [ ] No magic numbers or strings

### Error Handling
- [ ] Errors are caught and propagated, not swallowed
- [ ] Error messages are actionable and do not leak sensitive detail
- [ ] Logging is at the right level (no `debug` noise, no missing context)
- [ ] Edge cases and failure paths are handled
- [ ] No silent failures

### Performance
- [ ] No N+1 query problems
- [ ] Caching used where it pays off, not by reflex
- [ ] Database queries indexed and limited to needed columns
- [ ] Large payloads paginated
- [ ] No unnecessary work inside loops

### Testing
- [ ] New code has corresponding tests
- [ ] Tests cover the happy path and the edge cases
- [ ] Tests are readable and would fail if the behavior regressed
- [ ] No tests skipped without a stated reason
- [ ] Integration tests exist for critical paths

### Documentation
- [ ] Public APIs documented
- [ ] Non-obvious logic has a comment explaining *why*
- [ ] README or usage docs updated when behavior changes
- [ ] Breaking changes and migration steps called out

## Comment Templates

Use these when a one-line comment would be ambiguous. The outer fence is four backticks so the inner code block renders correctly.

````
🚨 Security Concern

Input in `[function/file]` is not validated before it reaches `[sink]`.
This allows [specific vulnerability, e.g. SQL injection / path traversal].

**Suggestion**: [specific fix, e.g. use parameterized queries].

**Example**:
```go
// Instead of:
query := "SELECT * FROM users WHERE id = " + userInput

// Use:
query := "SELECT * FROM users WHERE id = ?"
row := db.QueryRowContext(ctx, query, userInput)
```
````

````
⚡ Performance Note

The current implementation loads all records and filters in memory
(lines X-Y). With a large table this will not scale.

**Suggestion**: push the filter into the query.

**Example**:
```go
// Instead of:
users := getAllUsers()
filtered := filterUsers(users, cond)

// Use:
filtered := getUsersWhere(ctx, cond)
```
````

````
💡 Code Quality

The logic in lines X-Y is repeated in `[other file]`. Extracting it would
make future changes land in one place.

**Suggestion**:
```go
func processUserData(user *User) error {
    // extracted shared logic
}
```
````

## Best Practices

### For Reviewers
- ✅ Review promptly — a stale PR rots and conflicts
- ✅ Ask questions instead of making demands
- ✅ Praise good code; reviews are not only defect reports
- ✅ Be objective about the code, never personal about the author
- ✅ Suggest, don't command
- ✅ Approve early for good code; do not delay over minor issues

### For Authors
- ✅ Keep PRs focused and small (< 500 lines is a good target)
- ✅ Self-review the diff before requesting review
- ✅ Respond to every comment, even with "won't fix because …"
- ✅ Thank reviewers; their time is the scarce resource
- ✅ Fix issues rather than defend them
- ✅ Update the PR description when scope changes

## Pitfalls

### Bikeshedding
**Problem**: Time goes to style nits while logic and security go unreviewed.
**Solution**: Automate style with linters/formatters; order comments by impact (security > correctness > performance > style).

### Review Fatigue
**Problem**: Large PRs get a shallow "LGTM" because nobody can hold it all in their head.
**Solution**: Keep PRs small; split large features into reviewable chunks; use draft PRs for early feedback on direction.

### Hostile Reviews
**Problem**: Comments read as personal attacks and the discussion derails.
**Solution**: Use "we" language, focus on code not people, assume positive intent, and switch to a call when text stops working.

## Verification

After completing a review:
1. Every checklist section has been considered
2. Feedback is constructive and actionable
3. No blocking issues remain unresolved
4. The author understands what is being asked
5. Tests and coverage are adequate for the change

## Tools

- **Linters**: `golangci-lint`, ESLint, Pylint — automate style so humans review meaning
- **Security**: Snyk, OWASP ZAP, Semgrep
- **Coverage**: `go test -cover`, Jest, pytest-cov
- **PR templates**: configure in GitHub/GitLab/Bitbucket so context arrives with the diff

## References

- [Google's Code Review Developer Guide](https://google.github.io/eng-practices/review/)
- [Conventional Comments](https://conventionalcomments.org/)
- [SmartBear: Best Practices for Peer Code Review](https://smartbear.com/learn/code-review/best-practices-for-peer-code-review/)
