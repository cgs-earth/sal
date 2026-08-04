# `sal --help`: the subcommand list. Needs the tallest terminal of any tape, since
# go-arg prints one line per subcommand, and a wider one than the default so the
# longest description (`clone`, ~109 characters) does not wrap.

Output docs/public/demos/sal-help.gif

Source docs/demos/common.tape
Set Width 1320
Set Height 760

Source docs/demos/prompt.tape

Type "sal --help"
Sleep 300ms
Enter
Wait
Sleep 3s
