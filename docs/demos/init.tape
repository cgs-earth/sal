# `sal init`: shows the git remote SAL requires, answers the required
# description prompt, then what init creates.
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
# The description prompt ends in a colon, not the `$ ` the wait pattern looks
# for, so the answer is typed after a pause rather than after a Wait.
Sleep 1500ms
Type "A demo project for sal; a small dataset of stream gauges and the agency that runs them"
Sleep 300ms
Enter
Wait
Sleep 2s

Type "cat .gitignore"
Sleep 300ms
Enter
Wait
Sleep 2500ms
