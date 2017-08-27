watchevent
----------

`watchevent` watches filesystem, and if the files are changed,
sleep specified interval and invoke specified shell command.

## Options

```
Usage:
   watchevent [OPTIONS]
Options
  -c string
        config file
  -config string
        config file
  -d value
        directory to be watched
  -directory value
        directory to be watched
  -version
        show version
```

## Example

Create a config file:

```yaml
action:
  - name: write-logs
    on: [all]
    run: echo $(date) $WEV_EVENT $WEV_PATH >>log.txt

# zap logger's config (see zap document)
# https://github.com/uber-go/zap
log:
  level: "info"
  encoding: "console"
  #encoding: "json"
  encoderConfig:
    messageKey: "msg"
    levelKey: "level"
    timeKey: "time"
    nameKey: "name"
    callerKey: "caller"
    stacktraceKey: "stacktrace"
    levelEncoder: "capital"
    timeEncoder: "iso8601"
    durationEncoder: "string"
    callerEncoder: "short"
  outputPaths:
    - "stdout"
  errorOutputPaths:
    - "stderr"
```

Invoke `watchevent` command:

```
$ make
$ mkdir dir  # target directory
$ bin/watchevent -c config.yml -d dir/
```

And in another terminal:

```
$ touch dir/foo
```

It lets `watchevent` output logs like:

```
2017-08-27T21:38:10.785+0900    INFO    watchevent/watchevent.go:140      (1) Created file: dir/foo
2017-08-27T21:38:10.785+0900    INFO    watchevent/task.go:163  (1/1) Executing echo $(date) $WEV_EVENT $WEV_PATH >>log.txt ...
2017-08-27T21:38:10.785+0900    INFO    watchevent/watchevent.go:160      (2) File changed permission: dir/foo
2017-08-27T21:38:10.786+0900    INFO    watchevent/task.go:163  (2/1) Executing echo $(date) $WEV_EVENT $WEV_PATH >>log.txt ...
```

and `log.txt` content is:

```
Sun Aug 27 21:43:01 DST 2017 CREATE dir/foo
Sun Aug 27 21:43:01 DST 2017 CHMOD dir/foo
```
