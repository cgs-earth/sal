# `sal init`: shows the git remote SAL requires, then what init creates.
# Scenario state: fresh

Output docs/public/demos/sal-init.gif

Source docs/demos/common.tape
Source docs/demos/prompt.tape

Type "git remote -v"
Sleep 500ms
Enter
Sleep 1500ms

Type "sal init"
Sleep 500ms
Enter
Sleep 2500ms

Type "cat .gitignore"
Sleep 500ms
Enter
Sleep 3s
