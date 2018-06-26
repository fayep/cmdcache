package main

// vim: ts=2 sw=2 ai

import (
	"compress/gzip"
	"crypto/md5"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vmihailenco/msgpack"
)

// Exit struct is used to panic with an exit code
type Exit struct{ Code int }

// exit code handler
func handleExit() {
	if e := recover(); e != nil {
		if exit, ok := e.(Exit); ok == true {
			os.Exit(exit.Code)
		}
		panic(e) // not an Exit, bubble up
	}
}

// BufRecord is how we shunt data around
type BufRecord struct {
	T   int
	Id  int
	Buf []byte
}

// Timestamped is the type for our timestamped stream
type Timestamped struct {
	t  time.Time
	id int
	ch chan BufRecord
}

// OpenTS is how we setup a Timestamped stream
func OpenTS(id int, c chan BufRecord) *Timestamped {
	return &Timestamped{time.Now(), id, c}
}

// Write for Timestamped stream - as this is multiplexed,
// we send it to the channel for writing in one place.
func (t *Timestamped) Write(d []byte) (int, error) {
	tn := time.Now()
	dif := int64(tn.Sub(t.t) / time.Millisecond)
	t.t = tn
	data := []byte{}
	copy(data, d)
	rec := BufRecord{T: int(dif), Id: t.id, Buf: d}
	n := len(d)
	t.ch <- rec
	return n, nil
}

// DurationOf is so named because I was originally using a duration
// type, but the way of asserting the value was so unsatisfying
// Probably the messagepack library's fault.
// Here I work around the whole issue that msgpack won't tell me
// what type it's going to use to store the value by utilizing
// printf's reflective capabilities.  Anyway, since I am now using
// Milliseconds, it's still a duration, but not a Duration.
func DurationOf(i interface{}) int {
	var d int
	// There's probably a better way.
	s := fmt.Sprintf("%v", i)
	fmt.Sscanf(s, "%d", &d)
	return d
}

// plex marshals and sequences the record into the writer for the channel
// This is usually the same for both stdout and stderr so that's why
// we use the channel to keep writes coherent.
// f is usually a gzip writer with an actual file underneath
// they need time to finish flushing so we emit a finished signal
// which the main process blocks on.
func plex(f io.Writer, ch chan BufRecord, finished chan bool) {
	for {
		// Receive a record from the channel
		rec := <-ch
		if out, err := msgpack.Marshal(rec); err == nil {
			// Write a good record to the file
			f.Write(out)
			// If this is the exit code, we're done.
			if rec.Id == 127 {
				// Signal the end so that we can wait for it.
				finished <- true
				return
			}
		}
	}
}

func main() {
	// And the last shall be first... Using a panic handler means that
	// the defer Close() are executed before we finally os.Exit with our
	// desired exit code.
	defer handleExit() // plug the exit handler
	// Parameter flags
	ttlPtr := flag.Int("ttl", -1, "time to live in seconds")
	delayPtr := flag.Bool("delay", false, "'real time' display")
	keepNeg := flag.Bool("ve", false, "cache non-zero exit codes")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Printf(`Usage: %s [options] command [arguments]
Execute command with arguments and cache the output.
Output is stored with timestamps and interleaved so that you can
replay it again later.  Stdout and Stderr are preserved.
Options:
`, filepath.Base(os.Args[0]))
		flag.PrintDefaults()
		os.Exit(1)
	}
	// These are just the barewords after any options so they are
	// definitely the program and arguments to run.
	args := flag.Args()
	digest := md5.Sum([]byte(strings.Join(args, " ")))
	filename := fmt.Sprintf("%x", digest) + ".ts"
	location := path.Join(os.Getenv("HOME"), ".cmdcache", filename)

	if stat, err := os.Stat(location); err == nil &&
		(*ttlPtr == -1 ||
			int(time.Now().Sub(stat.ModTime())/time.Second) <= *ttlPtr) {
		f, _ := os.Open(location)
		defer f.Close()
		gz, _ := gzip.NewReader(f)
		defer gz.Close()
		mp := msgpack.NewDecoder(gz)
		var exitCode int
		for {
			if i, err := mp.DecodeInterface(); err == nil {
				// I don't bother to decode back to the original
				// Just assert the appropriate content.
				rec := i.(map[string]interface{})
				id := rec["Id"].(int8)
				buf := rec["Buf"].([]byte)
				// Do we want to slow down the output?
				if *delayPtr {
					T := DurationOf(rec["T"])
					time.Sleep(time.Duration(T) * time.Millisecond)
				}
				switch id {
				// stdout
				case 1:
					fmt.Printf("%s", string(buf))
				// stderr
				case 2:
					fmt.Fprintf(os.Stderr, "%s", string(buf))
				// exitcode
				case 127:
					exitCode = int(buf[0])
				}
			} else {
				// end of stream reached
				panic(Exit{exitCode})
			}
		}
	} else {
		// Create the file
		f, _ := os.Create(location)
		defer f.Close()
		// Setup the command to run
		cmd := exec.Command(args[0], args[1:]...)
		// Pass our standard input through
		cmd.Stdin = os.Stdin
		// Create pipes for stdout and stderr
		stdoutIn, _ := cmd.StdoutPipe()
		stderrIn, _ := cmd.StderrPipe()
		// Create channels for data and signalling
		ch := make(chan BufRecord)
		finished := make(chan bool)
		// Overlay compression on our file writer
		compressedStdout := gzip.NewWriter(f)
		defer compressedStdout.Close()
		// Setup tee to our timestamper and console
		stdout := io.MultiWriter(OpenTS(1, ch), os.Stdout)
		stderr := io.MultiWriter(OpenTS(2, ch), os.Stderr)
		// Start our program!
		er := cmd.Start()
		// Did we fail to start?
		if er != nil {
			// We shouldn't keep this file, it will erase properly on close.
			os.Remove(location)
			fmt.Fprintf(os.Stderr, "cmd.Start() failed with '%s'\n", er)
		}

		// Be ready to handle output created by below io.Copy
		go plex(compressedStdout, ch, finished)

		var errStdout, errStderr error

		// Copies until eof then exits
		go func() {
			_, errStdout = io.Copy(stdout, stdoutIn)
		}()

		// Copies until eof then exits
		go func() {
			_, errStderr = io.Copy(stderr, stderrIn)
		}()

		// Wait for command to exit
		er = cmd.Wait()
		exitCode := 0
		if exiterr, ok := er.(*exec.ExitError); ok {
			// The program has exited with an exit code != 0

			// This works on both Unix and Windows. Although package
			// syscall is generally platform dependent, WaitStatus is
			// defined for both Unix and Windows and in both cases has
			// an ExitStatus() method with the same signature.
			if status, ok := exiterr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			}
			// Did the user want to preserve error responses?
			if !*keepNeg {
				os.Remove(location)
			}
		}

		// Write an exit code to the stream
		ch <- BufRecord{T: 0, Id: 127, Buf: []byte{byte(exitCode)}}
		// Synchronize with everything written and the goroutine exited
		<-finished
		// Exit the program with an exit code, respecting defer
		panic(Exit{exitCode})
	}
}
