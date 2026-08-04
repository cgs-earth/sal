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
Set TypingSpeed 35ms
Set PlaybackSpeed 1

# VHS captures at 50fps by default, which is far more than a terminal recording
# needs and doubles what ffmpeg has to encode.
Set Framerate 24

# Tapes wait for the prompt to come back rather than sleeping for a guessed
# duration, so a slow CI runner can't finish a command after its Sleep has elapsed
# and leave the output cut off mid-frame. This pattern has to match the bare `$ `
# prompt set in prompt.tape; the optional space covers a trailing space being
# stripped from the captured line, and a pattern that never matches turns every
# Wait into a WaitTimeout stall.
Set WaitPattern /\$ ?$/
Set WaitTimeout 30s
