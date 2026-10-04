# Settings checks

The resolver reads explicit options and the selected TOML file. It does not
read retired setting environment variables. Its warning factory receives
only the presence of each retired name and supplies the replacement flag.
The complete file is checked before flags. Flags cannot hide an invalid
value, invalid combination, or invalid path in that file.

Each effective value has a default, file, or flag source. File records have
an absolute path and 1-based key line. Flag records have the canonical flag
and argument position. Default records include the declared value and rule.
The saved effective TOML contains every setting in registry order. Loading
that file uses new file provenance and preserves the typed values even when
the internal default table changes.

File-relative paths use the file directory. Flag-relative paths use cwd.
Context containment uses physical paths through symlink parents. Inactive
binary values remain in the record and have warnings. Only active binaries
reach the lookup function. Go's current-directory lookup refusal is kept.
Config handlers do not look up or launch an agent or Gashki.

Set and patch operations use the proven value-range adapter. A fixed lock
file protects cooperating writers. Before rename, the writer checks the
original content hash, inode, and lock identity. A conflict retains the
candidate. Empty patches create an absent empty file and otherwise do not
change the inode. Same-value changes keep original bytes and inode.

Editor arguments support quotes and backslash grouping. They have no shell
expansion. The editor receives a temporary copy. The lock is released while
the editor is open, then reacquired for comparison and commit. Invalid and
competing copies remain available. A production editor uses a separate
terminal descriptor; agent calls keep null input. Non-terminal editor input
is refused. CI and non-terminal calls cannot start an editor.

A-CFG-04 through A-CFG-08 use owned file fixtures and a local editor runner
spy. They prove settings and file behavior. Installed CLI grammar, repeated
vendor arguments, and doctor rendering remain checks in later tasks.
