watchevent
----------

watchevent watches filesystem, and if the files are changed,
sleep specified interval and invoke specified shell command.

## Options

```
Usage of watchevent:
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
```

## Example

```
$ make
$ bin/watchevent -c config.example.yml -d dir/
```

And in another terminal:

```
$ touch dir/foo
```
