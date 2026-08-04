# Settings shared by every SAL demo tape. Sourced, so it must not set Output.
#
# The prompt is reset to a bare `$` and the screen cleared inside a Hide block so
# that a recording never shows the recording machine's hostname, path, or shell
# theme, and looks the same locally as it does in CI.

Require sal

Set Shell "bash"
Set Theme "Catppuccin Mocha"
Set FontSize 18
Set Width 1200
Set Height 600
Set Padding 20
Set Margin 20
Set MarginFill "#1e1e2e"
Set BorderRadius 8
Set TypingSpeed 55ms
Set PlaybackSpeed 1
