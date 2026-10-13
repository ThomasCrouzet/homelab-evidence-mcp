# Project instructions

Read [CONTRIBUTING.md](CONTRIBUTING.md) before you change this project.
Keep its rules for code, tests, security, and pull requests.

## Documentation and commit messages

Use English for documentation, comments, docstrings, interface text, and user
messages. Translate French text in these areas into English. Do not make
duplicate English documents.
Keep content in other languages and translation catalogs.

Use the [ASD-STE100, Issue 9](https://www.asd-ste100.org/assets/files/ASD-STE100_ISSUE9.pdf)
reference for writing rules and vocabulary.
Examine each word against the dictionary. Examine its meaning and part of
speech.
Use short sentences, active voice, and concrete words.
Use one instruction in each sentence. Put conditions before instructions.
Limit procedure sentences to 20 words and descriptive sentences to 25 words.
Use one term for each concept. Keep necessary software terms.
If you cannot get the ASD-STE100 reference, use these principles. Give a
report of this limit. Do not give a report of full compliance without a full
rules and vocabulary inspection.

Use the same technical terms throughout the project. Examine each new
technical term against the ASD-STE100 rules for technical nouns and verbs.

Use these rules for future commit messages. Keep commit messages short and
imperative. Keep each required commit prefix.
Do not change Git history. Do not add automated attribution or signatures.
Do not use the Unicode character U+2014 in new content.

## Keep technical content

Use the same technical details and terminology as the code.
Keep behavior, identifiers, APIs, translation keys, commands, and executable
examples. Do not change vendored dependencies, generated files, licenses,
notices, or quoted text. If you must change a generated document, change its
source. Then run the document generator again.
Keep existing instructions and human attribution.

## Checks

Examine the diff, Markdown format, and links after documentation changes.
Make sure that commands and executable examples keep their behavior.
For source changes, use the applicable checks from the Makefile and CI:
`make lint`, `make test`, and `make build`.
Give changes, check results, and exceptions in the conversation.
Keep audit and review reports outside the repository. Do not commit them.

## Testing policy

- Never write unit tests after you write code.
- Highly prefer E2E tests as the sole testing mechanism.
- Use E2E tests to verify complex features through observable results.
- At the end of each E2E run, produce a verifiable and repeatable artifact.
- Record the command, source revision, environment, fixtures, and results
  with the artifact.
- Include the working diff identity when the source has uncommitted changes.
- Examine assertions, fixtures, mocks, and skips before removing a test.
- Do not treat disabled E2E tests or simulated boundaries as equivalent coverage.
- If isolation is necessary, first document all identified failure modes.
  Then write the tests and implementation.
- Keep an isolated test only for a concrete failure that E2E tests cannot detect.
- Do not add tests for coverage percentages, type contracts, dependency behavior,
  or mocked call sequences alone.

Run local checks with `GOMAXPROCS=2 GOFLAGS=-p=1 make test`.
`go test ./internal/mcpserver -run TestToolsViaMCPSession -count=1` uses
in-memory MCP and local HTTP fixtures.
It does not test the executable's stdio transport or live upstream services.
`go run ./demo` also checks fixture responses through in-memory MCP.
The demo checks redaction, instruction markers, and GET-only upstream requests.
Keep isolated checks for source decoding, freshness, truncation, budgets,
cache isolation, configuration, redaction, and destination restrictions.
The suite has no persistent E2E artifact directory.
Save the test log and fixture references outside the repository with the
required run metadata.
