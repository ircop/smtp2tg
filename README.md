# smtp2tg
SMTP 2 Telegram very simple relay

# Building
Building requires go version go1.6.1. You may use older versions, but without any warranty.

Before build, you must instal several packages:
```
go get gopkg.in/telegram-bot-api.v4
go get github.com/spf13/viper
```

And build program:
```
go build
```

# Running
Copy binary file to /usr/local/bin, or just run from building directory:

```
./smtp2tg
```
or
```
./smtp2tg -c /etc/smtp2tg.toml
```
If you want to listen 25 port, you need run program as root.


# Daemonizing
Unfortunately, golang has some problems with daemonizing: https://github.com/golang/go/issues/227

You can "daemonize" smtp2tg with system tools, like start-stop-daemon
