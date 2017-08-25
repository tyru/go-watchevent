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
	done := make(chan int)
	go poll(watcher, conf, done)

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

	return <-done
}

func invokeAction(cid CID,
	actionName string,
	conf *config.Config,
	event *fsnotify.Event,
	newEvent chan fsnotify.Op,
	done chan int) {

	action := conf.LookupAction(actionName)
	if action == nil {
		log.Printf("(%v) %s: Can't look up action", cid, actionName)
		done <- 20
		return
	}
	// Sleep
	log.Printf("(%v) Sleeping %s ...", cid, action.Interval)
	msec := config.MustParseIntervalMSec(action.Interval)
	timeout := time.After(time.Duration(msec) * time.Millisecond)
	select {
	case <-timeout:
		// Execute action
	case op := <-newEvent:
		intervalAction, err := action.DetermineIntervalAction(event.Op, op, config.Ignore)
		if err != nil {
			log.Println(err)
			log.Printf("(%v) %v ...\n", cid, action.Run)
			done <- 21
			return
		}
		if intervalAction == config.Ignore {
			// TODO: show intercepting event cid
			log.Printf("(%v) %s: ignored\n", cid, actionName)
			select {
			case <-timeout:
			}
			// Execute action
		} else if intervalAction == config.Retry {
			// TODO: show intercepting event cid
			log.Printf("(%v) %s: retried\n", cid, actionName)
			invokeAction(cid, actionName, conf, event, newEvent, done)
			return
		} else if intervalAction == config.Cancel {
			// TODO: show intercepting event cid
			log.Printf("(%v) %s: canceled\n", cid, actionName)
			return
		}
	}
	// Action
	log.Printf("(%v) Executing %s ...\n", cid, action.Run)
	exe := conf.Shell[0]
	cmd := exec.Command(exe, append(conf.Shell[1:], action.Run)...)
	cmd.Env = append(os.Environ(),
		"WEV_EVENT="+event.Op.String(),
		"WEV_PATH="+event.Name)
	err := cmd.Run()
	if err != nil {
		log.Printf("(%v) %v ...\n", cid, action.Run)
		done <- 22
		return
	}
}

func poll(watcher *fsnotify.Watcher, conf *config.Config, done chan int) {
	newEvent := make(chan fsnotify.Op, 1)
	for {
		select {
		case event := <-watcher.Events:
			handleEvent(&event, newEvent, watcher, conf, done)

		case err := <-watcher.Errors:
			log.Println("error: ", err)
			done <- 11
		}
	}
}

func handleEvent(event *fsnotify.Event, newEvent chan fsnotify.Op, watcher *fsnotify.Watcher, conf *config.Config, done chan int) {

	cid := makeCommandID()

	switch {
	case event.Op&fsnotify.Write == fsnotify.Write:
		log.Printf("(%v) Modified file: %s\n", cid, event.Name)
		sendNonBlock(newEvent, fsnotify.Write)
		if conf.OnWrite != "" {
			go invokeAction(cid, conf.OnWrite, conf, event, newEvent, done)
		}

	case event.Op&fsnotify.Create == fsnotify.Create:
		log.Printf("(%v) Created file: %s\n", cid, event.Name)
		sendNonBlock(newEvent, fsnotify.Create)
		// Watch a new directory
		if file, err := os.Stat(event.Name); err == nil && file.IsDir() {
			err = watchRecursively(event.Name, watcher)
			if err != nil {
				log.Fatal(err)
				done <- 10
			}
		}
		if conf.OnCreate != "" {
			go invokeAction(cid, conf.OnCreate, conf, event, newEvent, done)
		}

	case event.Op&fsnotify.Remove == fsnotify.Remove:
		log.Printf("(%v) Removed file: %s\n", cid, event.Name)
		sendNonBlock(newEvent, fsnotify.Remove)
		if conf.OnRemove != "" {
			go invokeAction(cid, conf.OnRemove, conf, event, newEvent, done)
		}

	case event.Op&fsnotify.Rename == fsnotify.Rename:
		log.Printf("(%v) Renamed file: %s\n", cid, event.Name)
		sendNonBlock(newEvent, fsnotify.Rename)
		if conf.OnRename != "" {
			go invokeAction(cid, conf.OnRename, conf, event, newEvent, done)
		}

	case event.Op&fsnotify.Chmod == fsnotify.Chmod:
		log.Printf("(%v) File changed permission: %s\n", cid, event.Name)
		sendNonBlock(newEvent, fsnotify.Chmod)
		if conf.OnChmod != "" {
			go invokeAction(cid, conf.OnChmod, conf, event, newEvent, done)
		}
	}
}

// Do not block when sending to channel
func sendNonBlock(ch chan fsnotify.Op, op fsnotify.Op) {
	select {
	case ch <- op:
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
