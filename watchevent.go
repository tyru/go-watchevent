package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"watchevent/config"

	"github.com/go-fsnotify/fsnotify"
)

var version = "x.y.z"

func main() {
	os.Exit(Main())
}

type Directories []string

func (dir *Directories) String() string {
	return strings.Join(([]string)(*dir), ",")
}

func (dir *Directories) Set(value string) error {
	*dir = append(*dir, value)
	return nil
}

func Main() int {
	// Parse args
	var configFile string
	flag.StringVar(&configFile, "config", "", "config file")
	flag.StringVar(&configFile, "c", "", "config file")
	var directories Directories
	flag.Var(&directories, "directory", "directory to be watched")
	flag.Var(&directories, "d", "directory to be watched")
	flag.Parse()

	if configFile == "" {
		fmt.Fprintln(os.Stderr, "[error] -config option is required")
		flag.Usage()
		return 2
	}
	if len(directories) == 0 {
		fmt.Fprintln(os.Stderr, "[error] one -d option is required at least")
		flag.Usage()
		return 3
	}

	// Load config
	conf, err := config.LoadConfig(configFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[error]", configFile, ": Could not load config file:", err)
		return 4
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintln(os.Stderr, "[error] Could not initialize watcher:", err)
		return 5
	}
	defer watcher.Close()

	// Run watcher
	exitAll := make(chan int)
	go poll(watcher, conf, exitAll)

	// Watch the specified directory
	for _, dir := range directories {
		if file, err := os.Stat(dir); err != nil || !file.IsDir() {
			fmt.Fprintln(os.Stderr, "[error]", dir, ": given path does not exist or not a directory:", err)
			return 6
		}
	}
	for _, dir := range directories {
		err := watchRecursively(dir, watcher)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[error]", dir, ": Could not watch directory:", err)
			return 7
		}
	}

	return <-exitAll
}

func invokeAction(invocation Invocation) {
	action := invocation.conf.LookupAction(invocation.actionName)
	if action == nil {
		log.Printf("(%v) [error] %s: Can't look up action", invocation.cid, invocation.actionName)
		invocation.done <- InvocationResult{
			exitCode:   20,
			invocation: invocation,
		}
		return
	}
	log.Printf("(%v) [info] Sleeping %s ...", invocation.cid, action.Interval)
	msec := config.MustParseIntervalMSec(action.Interval)
	go doInvocation(invocation, action, msec)
}

func doInvocation(invocation Invocation, action *config.Action, msec int64) {
	// Sleep
	timeout := time.After(time.Duration(msec) * time.Millisecond)
	select {
	case <-timeout:
		// Execute action
	case newInv := <-invocation.newInvocation:
		selfOp := invocation.event.Op
		newOp := newInv.event.Op
		intervalAction, err := action.DetermineIntervalAction(selfOp, newOp, config.Ignore)
		if err != nil {
			log.Println(err)
			log.Printf("(%v) [error] failed to execute '%v'\n", invocation.cid, action.Run)
			invocation.done <- InvocationResult{
				exitCode:   21,
				invocation: invocation,
			}
			return
		}
		if intervalAction == config.Ignore {
			// TODO: show intercepting event cid
			log.Printf("(%v) [info] %s: ignored\n", invocation.cid, invocation.actionName)
			select {
			case <-timeout:
			}
			// Execute action
		} else if intervalAction == config.Retry {
			// TODO: show intercepting event cid
			log.Printf("(%v) [info] %s: retried\n", invocation.cid, invocation.actionName)
			doInvocation(invocation, action, msec)
			invocation.done <- InvocationResult{
				exitCode:   0,
				invocation: invocation,
			}
			return
		} else if intervalAction == config.Cancel {
			// TODO: show intercepting event cid
			log.Printf("(%v) [info] %s: canceled\n", invocation.cid, invocation.actionName)
			invocation.done <- InvocationResult{
				exitCode:   0,
				invocation: invocation,
			}
			return
		}
	}
	// Action
	log.Printf("(%v) [info] Executing %s ...\n", invocation.cid, action.Run)
	exe := invocation.conf.Shell[0]
	cmd := exec.Command(exe, append(invocation.conf.Shell[1:], action.Run)...)
	cmd.Env = append(os.Environ(),
		"WEV_EVENT="+invocation.event.Op.String(),
		"WEV_PATH="+invocation.event.Name)
	err := cmd.Run()
	if err != nil {
		log.Printf("(%v) [error] failed to execute '%v'\n", invocation.cid, action.Run)
		invocation.done <- InvocationResult{
			exitCode:   22,
			invocation: invocation,
		}
		return
	}
	invocation.done <- InvocationResult{
		exitCode:   0,
		invocation: invocation,
	}
}

func poll(watcher *fsnotify.Watcher, conf *config.Config, exitAll chan<- int) {
	for {
		select {
		case event := <-watcher.Events:
			handleEvent(&event, watcher, conf, exitAll)

		case err := <-watcher.Errors:
			log.Println("error: ", err)
			exitAll <- 11
		}
	}
}

type Invocation struct {
	cid           CID
	conf          *config.Config
	event         *fsnotify.Event
	actionName    string
	newInvocation chan Invocation
	done          chan InvocationResult
}

type InvocationResult struct {
	exitCode   int
	invocation Invocation
}

var runningMutex sync.RWMutex = sync.RWMutex{}
var runningInvocations []Invocation

func NewInvocation(cid CID, conf *config.Config, event *fsnotify.Event, actionName string, exitAll chan<- int) Invocation {
	done := make(chan InvocationResult)
	go func() {
		result := <-done
		// Delete matched invocation in runningInvocations
		runningMutex.Lock()
		for i, invocation := range runningInvocations {
			if result.invocation.cid == invocation.cid {
				// Delete runningInvocations[i]
				runningInvocations = append(runningInvocations[:i], runningInvocations[i+1:]...)
				break
			}
		}
		runningMutex.Unlock()
		// Exit program when exitCode is non-zero
		if result.exitCode != 0 {
			exitAll <- result.exitCode
		}
	}()

	return Invocation{
		cid:           cid,
		conf:          conf,
		event:         event,
		actionName:    actionName,
		newInvocation: make(chan Invocation),
		done:          done,
	}
}

func handleEvent(event *fsnotify.Event, watcher *fsnotify.Watcher, conf *config.Config, exitAll chan<- int) {
	var actionName string
	cid := makeCommandID()

	switch {
	case event.Op&fsnotify.Write == fsnotify.Write:
		log.Printf("(%v) [info] Modified file: %s\n", cid, event.Name)
		actionName = conf.OnWrite

	case event.Op&fsnotify.Create == fsnotify.Create:
		log.Printf("(%v) [info] Created file: %s\n", cid, event.Name)
		// Watch a new directory
		if file, err := os.Stat(event.Name); err == nil && file.IsDir() {
			err = watchRecursively(event.Name, watcher)
			if err != nil {
				log.Fatal(err)
				exitAll <- 10
			}
		}
		actionName = conf.OnCreate

	case event.Op&fsnotify.Remove == fsnotify.Remove:
		log.Printf("(%v) [info] Removed file: %s\n", cid, event.Name)
		actionName = conf.OnRemove

	case event.Op&fsnotify.Rename == fsnotify.Rename:
		log.Printf("(%v) [info] Renamed file: %s\n", cid, event.Name)
		actionName = conf.OnRename

	case event.Op&fsnotify.Chmod == fsnotify.Chmod:
		log.Printf("(%v) [info] File changed permission: %s\n", cid, event.Name)
		actionName = conf.OnChmod
	}

	if actionName != "" {
		invocation := NewInvocation(cid, conf, event, actionName, exitAll)
		notifyNewInvocation(invocation)

		runningMutex.Lock()
		runningInvocations = append(runningInvocations, invocation)
		runningMutex.Unlock()

		invokeAction(invocation)
	}
}

func notifyNewInvocation(newInv Invocation) {
	runningMutex.RLock()
	defer runningMutex.RUnlock()
	for _, invocation := range runningInvocations {
		select {
		case invocation.newInvocation <- newInv:
		default:
		}
	}
}

type CID float64

var currentCID CID

func makeCommandID() CID {
	currentCID += 1
	return currentCID
}

func watchRecursively(root string, watcher *fsnotify.Watcher) error {
	err := watcher.Add(root)
	if err != nil {
		return err
	}
	log.Println("Watched:", root)

	files, err := ioutil.ReadDir(root)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.IsDir() {
			err := watchRecursively(filepath.Join(root, file.Name()), watcher)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
