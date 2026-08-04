# `sal query`: the DuckDB shell SAL opens over the built table, with its `triples`
# view already created.
# Scenario state: built

Output docs/public/demos/sal-query.gif

Source docs/demos/common.tape
Set Width 1360
Set Height 700
Source docs/demos/prompt.tape

Type "sal query"
Sleep 500ms
Enter
Sleep 6s

Type "SELECT predicate, object FROM triples WHERE predicate LIKE '%name%';"
Sleep 500ms
Enter
Sleep 5s

Type ".exit"
Sleep 500ms
Enter
Sleep 2s
