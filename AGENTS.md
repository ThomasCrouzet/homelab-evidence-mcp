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
