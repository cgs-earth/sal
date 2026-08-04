# `sal build`: writes the Iceberg table, then reruns to show that a build with no
# new triples does not create a second snapshot.
# Scenario state: committed

Output docs/public/demos/sal-build.gif

Source docs/demos/common.tape
Set Height 680
Source docs/demos/prompt.tape

Type "sal build data/"
Sleep 300ms
Enter
Wait
Sleep 2s

Type "sal build data/"
Sleep 300ms
Enter
Wait
Sleep 3s
