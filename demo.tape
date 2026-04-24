Output demo.gif

Require ./bin/ezoss

Set Shell "bash"
Set FontSize 20
Set Width 900
Set Height 600
Set Padding 20
Set Theme "Catppuccin Mocha"

Env EZOSS_NO_UPDATE_CHECK "1"
Env SHELL "/bin/bash"

Hide
Type "export HOME=/tmp/ezoss-demo-home && rm -rf /tmp/ezoss-demo-home && mkdir -p /tmp/ezoss-demo-home && clear"
Enter
Sleep 2s
Show

Type "# configure one repo and keep the setup local"
Sleep 2s
Ctrl+U
Sleep 1s
Type "./bin/ezoss init --repo kunchenguid/ezoss --agent auto"
Enter
Sleep 2s

Type "# start the mock daemon and let it fill the inbox"
Sleep 2s
Ctrl+U
Sleep 1s
Type "./bin/ezoss daemon start --mock"
Enter
Sleep 2s

Type "# check whether fresh recommendations are waiting"
Sleep 2s
Ctrl+U
Sleep 1s
Type "sleep 3 && ./bin/ezoss status"
Enter
Sleep 2s

Type "# inspect the private recommendation queue"
Sleep 2s
Ctrl+U
Sleep 1s
Type "./bin/ezoss list"
Enter
Sleep 3s

Type "# stop the daemon when you are done reviewing"
Sleep 2s
Ctrl+U
Sleep 1s
Type "./bin/ezoss daemon stop"
Enter
Sleep 2s

Type "# turn your issue queue into a reviewable inbox. ezoss"
Sleep 10s
