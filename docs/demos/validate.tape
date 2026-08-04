# `sal validate`: a passing directory, then an undefined term reported with its
# file and line number.
# Scenario state: initialized

Output docs/public/demos/sal-validate.gif

Source docs/demos/common.tape
Source docs/demos/prompt.tape

Type "sal validate data/"
Sleep 300ms
Enter
Wait
Sleep 1500ms

Type "cat drafts/gauge-03.jsonld"
Sleep 300ms
Enter
Wait
Sleep 2s

Type "sal validate drafts/"
Sleep 300ms
Enter
Wait
Sleep 3s
