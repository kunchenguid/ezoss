Output demo_raw.gif

Require ./bin/ezoss

Set Shell "bash"
Set FontSize 50
Set Width 2750
Set Height 1625
Set Padding 40
Set Theme "Catppuccin Mocha"
Set TypingSpeed 40ms

Env EZOSS_NO_UPDATE_CHECK "1"
Env SHELL "/bin/bash"

Hide
Type `export HOME=/tmp/ezoss-demo-home`
Enter
Type `rm -rf /tmp/ezoss-demo-home && mkdir -p /tmp/ezoss-demo-home`
Enter
Type `export PATH=$PWD/bin:$PATH`
Enter
Type `export PS1='$ '`
Enter
Type `clear`
Enter
Sleep 500ms
Show

Type `# point ezoss at a repo and pick an agent`
Sleep 1500ms
Enter
Type `ezoss init --repo kunchenguid/ezoss --agent auto`
Sleep 600ms
Enter
Sleep 2500ms

Type `# spin up the daemon (mock fixtures here)`
Sleep 1500ms
Enter
Type `ezoss daemon start --mock`
Sleep 600ms
Enter
Sleep 2000ms

Type `# wait a beat, then check the inbox`
Sleep 1500ms
Enter
Type `sleep 3 && ezoss status`
Sleep 600ms
Enter
Sleep 4500ms

Type `# open the TUI to review drafts`
Sleep 1500ms
Enter
Type `ezoss`
Sleep 600ms
Enter
Sleep 3000ms

Type "j"
Sleep 1500ms
Type "j"
Sleep 1500ms
Type "k"
Sleep 1500ms
Type "?"
Sleep 3000ms
Type "?"
Sleep 1500ms
Type "q"
Sleep 1500ms

Type `ezoss daemon stop`
Sleep 600ms
Enter
Sleep 2500ms

Type `# turn your issue queue into a reviewable inbox. ezoss`
Sleep 5000ms
