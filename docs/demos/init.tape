# `sal init`: shows the git remote SAL requires, then what init creates.
# Scenario state: fresh

Output docs/public/demos/sal-init.gif

Source docs/demos/common.tape
Source docs/demos/prompt.tape

Type "git remote -v"
Sleep 300ms
Enter
Wait
Sleep 1200ms

Type "sal init"
Sleep 300ms
Enter
Wait
Sleep 2s

Type "cat .gitignore"
Sleep 300ms
Enter
Wait
Sleep 2500ms
