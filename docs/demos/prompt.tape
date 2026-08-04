# Hidden shell setup shared by every tape: a bare `$` prompt in the demo project.
# Kept separate from common.tape because a few tapes need to size the terminal
# differently, and Set commands have to run before anything is typed.

Hide
Type "unset PROMPT_COMMAND; export PS1='\[\e[38;5;117m\]$\[\e[0m\] '" Enter
Type "cd ~/sal-demo" Enter
Type "clear" Enter
Show
