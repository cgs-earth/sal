# `sal query`: the SQL shell SAL opens over the built table, with its `triples`
# view already created.
# Scenario state: built

Output docs/public/demos/sal-query.gif

Source docs/demos/common.tape
Set Width 1360
Set Height 700

Source docs/demos/prompt.tape

Type "sal query"
Sleep 300ms
Enter
# The shell takes over the alternate screen and runs the statement it opened on,
# so there is no shell prompt for the default WaitPattern to match here.
Sleep 4s

# Ctrl+U clears the editor line the shell opened with, Ctrl+R runs what replaces it.
Ctrl+U
Sleep 500ms
Type "SELECT predicate, object FROM triples WHERE predicate LIKE '%name%'"
Sleep 300ms
Ctrl+R
Sleep 3s

Ctrl+D
Wait
Sleep 1500ms
