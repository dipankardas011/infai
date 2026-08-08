---
name: releaser
description: Standardized git tag and release workflow for infai
---

# Releaser

Load when creating a git tag (e.g., v0.70.0) or generating release notes.

## Workflow

1. Find previous tag: `git tag --sort=-v:refname | head -1`
2. List commits since: `git log --oneline <prev_tag>..HEAD`
3. Categorize into Features, Bug Fixes, Breaking Changes, Other
4. check the below tag message
5. Create: `git tag -a <version> -m "<message>"`
6. Push: `git push origin <version>`

## Tag message format

```
Release {{VERSION}}

Changelog:

{{#if FEATURES}}
Features:
{{#each FEATURES}}
- {{description}} ({{commit_hash}})
{{/each}}
{{/if}}

{{#if BUG_FIXES}}
Bug Fixes:
{{#each BUG_FIXES}}
- {{description}} ({{commit_hash}})
{{/each}}
{{/if}}

{{#if BREAKING_CHANGES}}
Breaking Changes:
{{#each BREAKING_CHANGES}}
- {{description}} ({{commit_hash}})
{{/each}}
{{/if}}

{{#if OTHERS}}
Other:
{{#each OTHERS}}
- {{description}} ({{commit_hash}})
{{/each}}
{{/if}}
```
