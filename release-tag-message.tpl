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
