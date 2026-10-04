# Prompt proof

The installed renderer uses embedded Markdown. It has no template directory
setting. The five typed markers are resolved in template source only. Inserted
user text is opaque. Unknown, unbalanced, and required unresolved markers fail
before a caller can deliver the prompt. The short baseline draft and closing
templates have no HUMAN marker, so their exact directive blocks are appended.

Every purpose appends the common review rule from the pinned Bash baseline.
The renderer records the base template hash, each used asset hash, their
canonical bundle hash, and the final prompt hash. Source validation does not
rescan inserted directives: a directive may itself quote template markers or
the review rule. Those quoted bytes must survive unchanged.

Only planner turns receive the backend question policy. Answer-record turns
instead instruct the planner to append HUMAN.md and preserve SPEC.md and
QUESTIONS.md. Only critic turns receive the selected rubric. Advisory turns
have explicit advisory scope. A verdict reminder requires a distinct logical
attempt identity and preserves the exact final-line verdict grammar. The
later executor must retain both attempt replies.

Skill planning records the effective skill folder, physical roots, and resolved
symlink targets, including nested links. It inventories paths without copying
skill contents. Requested unavailable skills fail before the standard can be
claimed. An unknown or absent optional root is reported as unavailable.

Claude receives Read rules and one system instruction naming its skill folder.
The [official permission documentation](https://code.claude.com/docs/en/permissions)
defines the double-slash absolute path prefix, gitignore path patterns, and
checks of both requested and resolved symlink paths. Literal path characters
are escaped before adding the descendant pattern. No skill or context root
receives an Edit or Write rule. Actual vendor enforcement belongs to the later
adapter and live checks.

Codex planning does not add Claude rules or change its identity environment.
The scratch helper accepts only a private, empty, caller-owned directory. It
validates all requested source links before mutation and creates symlinks only.
It does not copy config.toml. Partial exclusive creation failures retain their
owned artifacts. The caller must supply its resolved effective CODEX_HOME.

A-PROMPT-01 and A-PROMPT-02 use tier A F-PURE. The captured fixture has 180
combinations: nine purposes, two providers, two backends, and five rubric
choices. It includes exact question and answer bytes, constraints, context,
and all prompt hashes. Negative cases cover literal marker text, malformed
templates and requests, missing skills, linked files and directories, cyclic
links, absolute permission path grammar, and scratch root refusal. macOS and
Linux checks use isolated roots and no provider adapter or vendor process.
