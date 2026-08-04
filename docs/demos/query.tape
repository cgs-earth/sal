# `sal query`: the DuckDB shell SAL opens over the built table, with its `triples`
# view already created.
# Scenario state: built

Output docs/public/demos/sal-query.gif

Source docs/demos/common.tape
Set Width 1360
Set Height 700

# In a TTY, DuckDB pipes its output through a pager. Left alone the recording shows
# `less` rather than the shell, and the `.exit` below goes to the pager instead of
# DuckDB. Only the paging is suppressed; the output itself is unchanged.
Env PAGER cat

Source docs/demos/prompt.tape

Type "sal query --info properties"
Sleep 300ms
Enter
# Inside the DuckDB shell the prompt is `<database> D `, not the `$ ` that the
# default WaitPattern in common.tape matches.
Wait /D ?$/
Sleep 1500ms

Type "SELECT predicate, object FROM triples WHERE predicate LIKE '%name%';"
Sleep 300ms
Enter
Wait /D ?$/
Sleep 2500ms

Type ".exit"
Sleep 300ms
Enter
Wait
Sleep 1500ms
