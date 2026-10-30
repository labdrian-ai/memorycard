# Security policy

## Supported versions

Security fixes go into the latest release. Older releases are not patched.

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub:
[Report a vulnerability](https://github.com/labdrian-ai/memorycard/security/advisories/new).
Do not open a public issue for a security problem.

Include what you found, how to reproduce it, and the version (`memorycard
version`). You will get an answer within 7 days. Once a fix is released, the
advisory is published with credit to the reporter, unless you prefer otherwise.

## Scope

memorycard runs locally. It reads and writes one SQLite file
(`MEMORYCARD_DB`, default `~/.memorycard/memory.db`) and makes network calls
only to the embedder (`MEMORYCARD_OLLAMA_URL`, default
`http://localhost:11434`). Reports that matter most:

- reading or writing files other than the database and the paths you pass on
  the command line (for example `import engram <path>`);
- network traffic to anything other than the configured embedder;
- crashes or corruption caused by crafted input to the MCP tools or the CLI.
