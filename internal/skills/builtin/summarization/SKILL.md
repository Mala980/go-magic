---
name: summarization
description: "Use when the user asks for a summary, TL;DR, abstract, or executive summary of a document, article, web page, or long conversation, or when long content needs to be condensed to its key points at a requested length."
version: 2.0.0
author: go-magic
tags: [summarization, content, writing, analysis]
tools: [web_fetch, read_file]
---

# Summarization Skill

Condense content faithfully: shorter, but not different.

## When to Use

Load this skill when:
- The user asks for a summary of a document, article, page, or thread
- Long content needs to be reduced to key points
- An abstract or executive summary is required
- Meeting notes or a long conversation need distilling

## Getting the Content

- Local file → `read_file` (check length; read in full or in ordered chunks)
- URL → `web_fetch`
- Content already in the conversation → use it directly, do not re-fetch

Read the whole thing before summarizing. Summarizing the first third and extrapolating is how fabricated conclusions get introduced.

## Two Approaches

### Extractive
Select and stitch the most important existing sentences. Faithful, but can read disjointedly. Use when precision matters more than flow (legal, technical, quotes).

### Abstractive
Rephrase and synthesize into new sentences. Reads better and is shorter, but risks drift. Use for narrative and explanatory content — and keep checking your wording against the source.

Most good summaries mix both: extract the facts, write the connective tissue.

## What a Summary Must Contain

1. **Main topic / thesis** — what is this about, in one sentence
2. **Key points** — 3-5, in the source's own order of importance
3. **Supporting evidence** — the specific numbers, findings, or examples that carry the argument
4. **Conclusion / recommendation** — what the source concludes or asks for

## Structure

```
## Summary

**Topic**: [one sentence]

**Key Points**:
1. ...
2. ...
3. ...

**Conclusion**: [final takeaway]

**TL;DR**: [one sentence]
```

## Length Guidelines

| Original Length | Summary Length |
|-----------------|----------------|
| < 500 words     | 50-100 words   |
| 500-2000 words  | 100-300 words  |
| 2000-5000 words | 200-500 words  |
| > 5000 words    | 500-1000 words |

When the user states a length or format, that wins over the table.

## Best Practices

- ✅ Read the entire content first
- ✅ Lead with the thesis, not with background
- ✅ Preserve the source's stance and uncertainty ("the study suggests", not "it is proven")
- ✅ Keep the numbers; round only if the source did
- ✅ Attribute claims when the source attributes them
- ✅ Match the requested length and audience

## What to Avoid

- ❌ Adding your own opinion or recommendation
- ❌ Changing the meaning, strength, or certainty of a claim
- ❌ Including every detail (that is a compression, not a summary)
- ❌ Pulling quotes out of context
- ❌ Stating a conclusion the source never reached
- ❌ Hiding that something is unclear — say "the source is ambiguous here"

## Verification

Before returning a summary:
1. Every key point traces back to a specific part of the source
2. No claim is stronger than the source's wording
3. The length matches the request
4. Someone who reads only the summary would not be misled about the conclusion
