# Legacy boundary and explicit resolution proof

The T19 proof covers A-MIG-01 through A-MIG-04. Its aggregate SHA-256 is
`cab7a1a934c46fdf9bb60e39a51ab2bbee0fcb1792eb5040f2556afa9bbd5ce3`.
The compact proof contains source and binary hashes, target OS, settings,
observed output, and artifact hashes. The installed command proof hashes are:

- macOS: `a971cb436224e003cd3c8f98acf7a18115081c68d410d3d025b2ef9bfa3a4631`
- Linux: `df5eab7ed93d29e6a3cb2c456f469275f81da4202d506d4fad3f0f533bc1f579`

File and process checks pass on both supported operating systems. Strict input
and policy checks run on macOS. The installed proof uses the Go executable and
its native launcher names. It checks active, completed and uncertain legacy
fixtures, deterministic reports, rejected import flags, safe no-argument help,
and a reserved round output collision. All legacy and collision checks make
zero dependency calls and preserve every source file.

The file checks cover each legacy marker alone, empty and partial contents,
symlink and directory markers, marker-free rounds, and invalid or unsupported
native manifests. No unsafe marker target is read. Historical approval text has
no native authority.

The resolution checks use file-backed fake evidence and session interfaces.
They check artifact changes, active sessions, missing delivery proof and missing
process proof. A human statement cannot permit retry. A fake checked completion
without independent backend completion writes `human_attested` provenance.
Checks precede advance. The native direct checks accept an existing checked
receipt without another model turn, retain abandoned artifacts, and permit later
resume only after checkpoint-bound proof of a real process start failure. The
runner fails to start an owned executable whose execute permission is removed
for that call. It does not substitute a required answer.

A fresh review from a preserved legacy seed and explicit constraints starts with
critic round 1, then revises and approves. The legacy source and session record
remain intact. The mutable spec has a separate inode from its immutable source.
A focused publication interruption check verifies recovery after the copy link.
One existing controller crash check at receipt publication also passes. The
changed packages pass `go vet`.

The real Gashki evidence recovery remains part of T22. This proof makes no live
vendor conversation and does not switch the source Bash launchers to Go.

Routine fixtures auto-delete. Only compact evidence and logs remain. Linux
checks use a disk filesystem, a 2 GiB free-space reserve and a 512 MiB fixture
budget. The failed early test attempts are excluded from the passing proof.
