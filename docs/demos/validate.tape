# `sal validate`: a passing directory, then an undefined term reported with its
# file and line number.
# Scenario state: initialized

Output docs/public/demos/sal-validate.gif

Source docs/demos/common.tape
Source docs/demos/prompt.tape

Type "sal validate data/"
Sleep 500ms
Enter
Sleep 3s

Type "cat drafts/gauge-03.jsonld"
Sleep 500ms
Enter
Sleep 2500ms

Type "sal validate drafts/"
Sleep 500ms
Enter
Sleep 5s
