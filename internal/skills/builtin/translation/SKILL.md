---
name: translation
description: "Use when the user asks to translate text between languages, localize an application or website, or understand content in a foreign language, including preserving tone, formatting, and technical terminology."
version: 2.0.0
author: go-magic
tags: [translation, languages, localization, i18n]
tools: [web_fetch, read_file]
---

# Translation Skill

Translate meaning, not words — and preserve the tone and structure of the original.

## When to Use

Load this skill when:
- The user asks for text to be translated
- Localizing an application, website, or UI strings
- Understanding foreign-language content
- Keeping terminology consistent across a multi-language project

## Process

### 1. Understand the Source
- Read the full context, not isolated sentences
- Identify the register: formal / informal, technical / marketing / casual
- Note idioms, cultural references, and domain terms
- Determine whether anything is untranslatable as-is (wordplay, units, names)

### 2. Draft
- Translate for meaning first; literal structure second
- Keep placeholders, variables, and markup untouched (`%s`, `{name}`, `<b>`, markdown)
- Flag anything ambiguous rather than silently guessing

### 3. Localize
- Adapt idioms to an equivalent in the target language, not a word-for-word calque
- Respect locale conventions: date, number, currency, unit, address format
- Match formality to the audience
- Keep a glossary so a term is translated the same way everywhere

### 4. Review
- Accuracy: no meaning added, dropped, or strengthened
- Flow: reads as if written in the target language
- Consistency: same term, same translation, throughout
- Completeness: nothing left untranslated by accident

## Translation Modes

| Mode | Use for | Trade-off |
|---|---|---|
| Literal | Legal, contractual, reference | Faithful, can read stiff |
| Idiomatic / dynamic | Marketing, UI, conversation | Natural, more interpreter judgment |
| Technical | Documentation, API reference, specs | Precise terminology, keep source terms where standard |

## Best Practices

- ✅ Preserve the original meaning and level of certainty
- ✅ Maintain tone and style (do not make a formal text casual, or vice versa)
- ✅ Keep technical terms consistent; keep the English term in parentheses on first use when there is no settled equivalent
- ✅ Preserve formatting, headings, and code blocks exactly
- ✅ Translate UI strings to fit the space they occupy (button labels matter)

## Common Challenges

### Idioms
Translate the intent, not the words.

```
EN  "Break a leg"      → ZH "祝你好运"          (not a literal "断条腿")
EN  "Bite the bullet"  → ZH "硬着头皮上"
ZH  "画蛇添足"          → EN "gilding the lily"
```
If there is no equivalent idiom, translate plainly and, if the idiom matters to the sentence, add a short gloss.

### Cultural references
Replace an untranslatable reference with one the target audience shares, or explain it briefly. If neither works, keep it and gloss it.

### Formality
Many languages encode politeness in the pronoun or verb form; English does not. Pick the register from the context and stay consistent:
- French: `tu` vs `vous`
- German: `du` vs `Sie`
- Japanese: plain form vs 敬語 (keigo)
- Chinese: 你 vs 您

### Terminology
- Established translations exist → use them (`interface` → 接口, `commit` → 提交)
- No settled term → keep the source term, translated on first use
- Product names, code identifiers, and error codes → do not translate

## Verification

1. Back-translate a sample sentence in your head — does it land on the original meaning?
2. Check placeholders, code blocks, and numbers survived unchanged
3. Check one term appears the same way throughout
4. Check the register matches the source

## Tools Integration

`web_fetch` and `read_file` to pull the source content; translate within this framework rather than re-fetching the target.
