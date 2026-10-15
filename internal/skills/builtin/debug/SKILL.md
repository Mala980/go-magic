---
name: debug
description: "Use when a program crashes, panics, hangs, races, or behaves unexpectedly, when reading a stack trace or log file to find a root cause, or when investigating a performance problem such as high CPU, memory growth, or slow requests."
version: 2.0.0
author: go-magic
license: MIT
tags: [debug, error-analysis, logging, profiling, troubleshooting]
tools: [read_file, search_in_files, analyze_error, execute_command]
---

# Debug Skill

Systematic approach to finding root causes in errors, logs, and performance problems.

## When to Use

Load this skill when:
- Debugging a crash, panic, or hang
- Reading a stack trace or a log file to locate a failure
- Investigating a performance problem (CPU, memory, latency)
- A bug is intermittent and you suspect concurrency
- You need a reproduction or a minimal failing case

## Capabilities

### 1. Error Analysis
- **Stack traces**: read from the bottom-most frame in *your* code upward
- **Pattern recognition**: classify before fixing — nil deref, type error, resource exhaustion, race
- **Root cause**: keep asking "why" until the answer is a design flaw, not a symptom
- **Severity**: separate "system down" from "cosmetic" before choosing response

### 2. Log Analysis
- Structured formats (JSON, logfmt) over free text
- Filter by level first, then by time window around the failure
- Correlate across services with a request ID
- Look for the *absence* of a log line as evidence too

### 3. Strategies
- **Reproduce first** — a bug you cannot reproduce is a bug you cannot verify a fix for
- **Bisect**: over commits, over input size, over config toggles
- **Minimize**: shrink to the smallest input and program that still fails
- **Rubber-duck**: explain the code line by line; the wrong assumption usually surfaces
- **Change one variable at a time**

### 4. Performance
- Profile before optimizing; guesswork tunes the wrong line
- CPU: sampling profiler / flame graph
- Memory: heap profile, look for growth that never returns to baseline
- I/O: trace syscalls and external calls; look for serial round trips
- Concurrency: run with the race detector

## Error Classification

| Severity | Description | Response Time |
|----------|-------------|---------------|
| Critical | System down, data loss or corruption | Immediate |
| High | Major feature broken | < 1 hour |
| Medium | Feature degraded, workaround exists | < 4 hours |
| Low | Minor issue | < 24 hours |

### Common Patterns

#### Nil / Undefined Dereference
```
Go:     panic: runtime error: invalid memory address or nil pointer dereference
JS:     TypeError: Cannot read properties of undefined (reading 'x')
Python: AttributeError: 'NoneType' object has no attribute 'x'
Cause:  a value that is expected to exist is nil/None/undefined on this path
Fix:    validate at the boundary; return early; do not paper over with a catch-all
```

#### Type Errors
```
Go:     interface conversion: interface {} is string, not int
JS:     TypeError: x is not a function
Python: TypeError: unsupported operand type(s)
Cause:  wrong type reaching the operation, often via an untyped boundary (JSON, env, flags)
Fix:    validate/convert at the boundary, not at every use site
```

#### Resource Exhaustion
```
Pattern: out of memory, too many open files, connection timeout
Cause:   leak (unclosed resource, unbounded cache, listener never removed) or genuine capacity limit
Fix:    fix the leak first; only then raise the limit
```

#### Race Conditions
```
Pattern: intermittent failure, different result per run, corrupted shared state
Cause:   unsynchronized access, or a goroutine outliving its data
Fix:     run with -race; add the minimum synchronization; prefer passing data over sharing
```

## Log Format Reference

### Levels
```
DEBUG: detail useful only while diagnosing
INFO:  normal operational events
WARN:  something unexpected that did not break the operation
ERROR: the operation failed
FATAL: the process is going down
```

### Shaping
```
Go:     2024-01-15T10:30:00Z ERROR handler.go:42: failed to process request: connection timeout
        TIMESTAMP LEVEL FILE:LINE: MESSAGE

Python: 2024-01-15 10:30:00,000 - module.name - ERROR - failed to process request
        TIMESTAMP - LOGGER - LEVEL - MESSAGE

Node:   [2024-01-15T10:30:00.000Z] ERROR (app/12345): failed to process request
        [TIMESTAMP] LEVEL (CONTEXT): MESSAGE
```

### Correlation
Every log line on a request path should carry a request ID and, where relevant, a user ID:
```
request_id=abc123 user_id=456 method=GET path=/api/users duration_ms=42
```

## The DEBUG Method

1. **D**escribe — what happens? what should happen? when did it start?
2. **E**xamine — recent changes? environment differences? does it reproduce elsewhere?
3. **B**reak down — which component fails? can you isolate it? minimal case?
4. **U**nderstand — trace the flow, check each assumption, verify inputs and outputs
5. **G**enerate hypotheses — list candidates, rank by likelihood, design a test for each

### Checklist
```
[ ] Reproduce the issue reliably
[ ] Check recent code changes (git log / git bisect)
[ ] Verify configuration and environment
[ ] Read the relevant logs (level filter, then time window)
[ ] Check resource usage (CPU, memory, disk, file descriptors)
[ ] Verify network / dependency reachability
[ ] Reduce to the minimal failing case
[ ] Check for races (-race / thread sanitizer)
[ ] Verify the error-handling path is actually reachable
```

## Profiling

> Language-specific tooling has dedicated skills when present: for Go, a workspace `debug-go` skill (Delve, pprof deep usage, goleak, GOTRACEBACK) may be available — load it for driving the tools, not just reading a profile.

### Go
```bash
# CPU
go test -cpuprofile=cpu.prof -bench=.
go tool pprof -http=:8080 cpu.prof

# Memory
go test -memprofile=mem.prof -bench=.
go tool pprof -http=:8080 mem.prof

# Race detection
go test -race ./...
go run -race main.go

# Live endpoint (import _ "net/http/pprof")
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30
```

### Python
```bash
python -m cProfile -o out.prof script.py
kernprof -l -v script.py            # line_profiler
python -m memory_profiler script.py
```

### Node.js
```bash
node --prof script.js && node --prof-process isolate-*.log
clinic doctor -- node script.js
clinic flame -- node script.js
```

### Thresholds and Symptoms

| Metric | Good | Warning | Critical |
|--------|------|---------|----------|
| CPU usage | < 70% | 70-85% | > 85% |
| Memory usage | < 70% | 70-85% | > 85% |
| p99 response time | < 200ms | 200-500ms | > 500ms |
| Error rate | < 0.1% | 0.1-1% | > 1% |

```
Memory leak:  gradual rise that never returns to baseline -> heap profile, unclosed resources, unbounded caches
CPU hotspot:  high CPU, flat latency -> flame graph, look for accidental O(n^2) and per-request compilation
I/O stall:    waiting on dependencies -> trace the calls, look for serial round trips and missing caching
```

## Worked Examples

### Go panic
```
panic: runtime error: invalid memory address or nil pointer dereference
goroutine 1 [running]:
main.processUser(0x0, 0xc000012345)
    /app/main.go:42
main.main()
    /app/main.go:15

Analysis:
1. Nil dereference at main.go:42 — processUser was called with a nil *User
2. main.go:15 is the call site; the value came from a lookup whose "not found" case is ignored
3. Fix at the source: handle the not-found branch, do not add a nil check at :42 to hide it
```

### Python exception
```
Traceback (most recent call last):
  File "/app/handler.py", line 42, in handle_request
    result = process_data(data)
  File "/app/processor.py", line 15, in process_data
    return data['key'] / data['value']
ZeroDivisionError: division by zero

Analysis:
1. data['value'] is 0 (or missing and defaulted to 0)
2. Validate the input contract where the payload is parsed, not at the division
3. Decide the domain answer: reject the input, or define a fallback value
```

## Best Practices

### Logging
1. Prefer structured logs over formatted strings
2. Always include request/user IDs and timestamps
3. Log at the level that matches the consequence, not the author's mood
4. Never log secrets, tokens, or full request bodies
5. Log once at the boundary, not at every layer of the stack

### Debugging
1. Reproduce before you theorize
2. Change one thing at a time
3. Keep the fix reviewable; revert debug scaffolding before committing
4. Write the regression test that fails without the fix
5. Record the root cause so the next person does not re-derive it

## Emergency Response

### Critical failure
1. Assess impact — how many users, how much data
2. Mitigate first (rollback, feature flag, capacity) — diagnose after the bleeding stops
3. Find the root cause with production data preserved
4. Fix, test, deploy with monitoring
5. Write the post-mortem while the timeline is still fresh

### Performance emergency
1. Identify the saturated resource (CPU, memory, I/O, connections)
2. Shed or shift load (rate limit, queue, scale out)
3. Fail fast on unhealthy dependencies instead of queueing behind them
4. Optimize only the hot path, and only with profile evidence
