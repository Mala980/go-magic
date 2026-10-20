---
name: coding
description: "Use when writing, optimizing, or refactoring code, when choosing a design pattern for a problem, or when a change needs to be made faster, smaller, or clearer. Covers optimization, design patterns and refactoring — not review conventions, which live in the code-review skill."
version: 2.0.0
author: go-magic
license: MIT
tags: [coding, optimization, design-patterns, refactoring]
tools: [read_file, search_in_files, lint, format_code, analyze_error, diff_patch, batch_file_ops]
---

# Coding Skill

Guidance for writing, optimizing, and refactoring code.

## When to Use

Load this skill when:
- Writing new code or restructuring existing code
- Optimizing for time, space, or I/O
- Choosing a design pattern and needing to know when it fits
- Refactoring without changing behavior
- Reviewing code for correctness, performance, or maintainability

For review conventions, comment formats, and team process, use the **code-review** skill instead — that is its own skill and this one does not duplicate it.

## Capabilities

### 1. Correctness and Review

Reason about, and look for, problems in this order:
- **Correctness**: logic errors, edge cases, unhandled failure paths
- **Concurrency**: races, deadlocks, unbounded goroutine fan-out
- **Resource handling**: every opened file/connection/body has a matching `defer` close
- **Performance**: algorithm complexity, allocation in hot paths
- **Maintainability**: clarity, naming, testability

Report concrete findings. For the checklist and comment format, defer to **code-review**.

### 2. Optimization

Optimize in this order, and only after measuring:
- **Time complexity**: better algorithms first, caching second
- **Space complexity**: fewer live allocations, right data structure
- **I/O**: batching, streaming, async where it pays off
- **Concurrency**: parallelize independent work; bound the parallelism
- **Database**: indexes, projected columns, N+1 elimination

Rules: profile before optimizing; change one thing at a time; keep the benchmark beside the change so the win is provable and regression-visible.

### 3. Design Patterns

Use a pattern when the problem already exists — not in anticipation of one.

#### Creational
- **Singleton**: exactly one instance with a lifecycle longer than a request
- **Factory**: callers should not know which concrete type they get
- **Builder**: construction with many optional pieces
- **Prototype**: cloning is cheaper than constructing

#### Structural
- **Adapter**: reconcile an interface you cannot change
- **Decorator**: add behavior without subclassing
- **Facade**: expose a simpler surface over a messy subsystem
- **Proxy**: control access or defer cost

#### Behavioral
- **Observer**: many consumers of one event stream
- **Strategy**: swap an algorithm behind a stable interface
- **Command**: turn a request into a value (queue, log, undo)
- **Chain of Responsibility**: pipeline of handlers, each may stop or pass on

### 4. Refactoring

- **Extract Function / Method**: name a block that deserves a name
- **Inline**: remove indirection that no longer earns its keep
- **Move**: put behavior next to the data it uses
- **Rename**: fix names that lie about intent
- **Replace conditional with polymorphism**: when the same switch keeps growing

#### Process
- Before: have tests that pass; state the goal; plan incremental steps
- During: small changes; run tests after each; commit per step
- After: tests green; review the diff for new problems; update docs; consider performance

## Best Practices by Language

### Go
- `gofmt` is not optional; `go vet` and `golangci-lint` in CI
- Handle every error explicitly; wrap with `%w` to preserve the chain
- Accept interfaces, return structs
- Prefer composition over inheritance
- Communicate via channels; guard shared state with `sync` primitives — and always run tests with `-race`
- Bound goroutines; always have a way for a spawned goroutine to exit

### JavaScript / TypeScript
- ESLint + Prettier
- `const` by default, `let` only when reassigned
- `async`/`await` with handled rejections; never leave a floating promise
- TypeScript for anything that outlives a single file

### Python
- PEP 8; type hints on public functions
- Comprehensions over manual accumulation
- Catch specific exceptions; never bare `except:`
- Context managers for every resource

### Rust
- `cargo clippy` in CI
- Ownership and borrowing instead of cloning to silence the compiler
- `Result`/`Option` with `?`; `unsafe` only with a written justification

## Anti-Patterns to Avoid

### Code Smells
- **God Object**: one type that knows everything
- **Feature Envy**: a method that mostly touches another type's data
- **Primitive Obsession**: raw strings/ints where a type would prevent bugs
- **Growing switch**: same `switch` on the same tag in several places
- **Temporary Field**: fields that only matter in one state

### Performance Anti-Patterns
- **Premature optimization**: tuning before measuring
- **Allocation churn**: building objects in a hot loop
- **Blocking I/O**: synchronous calls inside an async/goroutine pipeline
- **Leaks**: unclosed resources, unbounded caches, listeners never removed
- **N+1 queries**: a query per element of a collection

## Worked Examples

### Security: parameterize the query
```go
// Bad
query := "SELECT * FROM users WHERE id = " + userID

// Good
query := "SELECT * FROM users WHERE id = ?"
rows, err := db.QueryContext(ctx, query, userID)
```

### Performance: remove the N+1
```go
// Bad - one query per user
for _, user := range users {
    orders := getOrders(ctx, user.ID)
    _ = orders
}

// Good - one query for the whole set
ordersByUser := getOrdersForUsers(ctx, userIDs)
```

### Refactoring: replace a growing switch
```go
// Before
func process(data []Data) {
    for i := 0; i < len(data); i++ {
        if data[i].Type == "A" {
            processA(data[i])
        } else if data[i].Type == "B" {
            processB(data[i])
        }
    }
}

// After
func process(data []Data) {
    for _, d := range data {
        processByType(d)
    }
}

func processByType(d Data) {
    switch d.Type {
    case "A":
        processA(d)
    case "B":
        processB(d)
    }
}
```

## Tools Integration

This skill works with the registered tools:
- `lint`: run linters over files or a project
- `analyze_error`: parse a stack trace or error into causes and next steps
- `read_file` / `search_in_files`: read and locate code
- `diff_patch`: apply a focused edit
- `batch_file_ops`: multiple file operations in one call
