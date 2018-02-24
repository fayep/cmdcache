# CmdCache

CmdCache is a commandline memoization tool.  You use AWS, don't you?
Responses are stored compressed, timestamped and with stdout and stderr
interleaved.  The return code is also preserved in the cached item which
is useful if you're caching `-ve` responses.

I wrote this program to solve a problem (not hammering the AWS API every time I wanted to use it) and also to learn some core Golang techniques.

Notable features include:

* Exit code return which respects deferred actions
* Using a channel to multiplex safely
* Using a channel to synchronize with a background task
* Making a "Writer" to specify how to serialize my data, simplifying the code to a copy operation.

## To Do

* Move the timestamping to the plex function or otherwise take account of the stream offsets being based on different source times

## Install

```
$ go get github.com/fayep/cmdcache
```

## Usage

```
$ cmdcache [-ve] [-delay] [-ttl <s>] <command> <arguments> 
```
| Option | Meaning |
| ------ | ------- |
| `-ve`    | Also cache negative responses (non zero exit)  |
| `-delay` | delay cached output based on stored timestamps |
| `-ttl <s>`   | the number of seconds before a cached result is refreshed |

## Contribute

PRs accepted.

## License

MIT
(C)2018 Faye Salwin

