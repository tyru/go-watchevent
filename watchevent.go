package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-fsnotify/fsnotify"
)

var version = "x.y.z"

func main() {
	os.Exit(Main())
}

func Main() int {

	flag.Usage = func() {
		name := filepath.Base(os.Args[0])
		fmt.Fprintf(os.Stderr, `Usage of %s:
   %s [OPTIONS]
Options
`, name, name)
		flag.PrintDefaults()
	}

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
	conf, err := LoadConfig(configFile)
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

type Directories []string

func (dir *Directories) String() string {
	return strings.Join(([]string)(*dir), ",")
}

func (dir *Directories) Set(value string) error {
	*dir = append(*dir, value)
	return nil
}

func poll(watcher *fsnotify.Watcher, conf *Config, exitAll chan<- int) {
	coodinator := NewTaskCoodinator()
	for {
		select {
		case event := <-watcher.Events:
			handleEvent(&event, watcher, conf, coodinator, exitAll)

		case err := <-watcher.Errors:
			log.Println("error: ", err)
			exitAll <- 11
		}
	}
}

func handleEvent(
	event *fsnotify.Event,
	watcher *fsnotify.Watcher,
	conf *Config,
	coodinator *TaskCoodinator,
	exitAll chan<- int,
) {
	eid := makeEventID()
	var actions []*Action

	switch {
	case event.Op&fsnotify.Write == fsnotify.Write:
		log.Printf("(%v) [info] Modified file: %s\n", eid, event.Name)
		actions = conf.GetActionsOn("write")

	case event.Op&fsnotify.Create == fsnotify.Create:
		log.Printf("(%v) [info] Created file: %s\n", eid, event.Name)
		// Watch a new directory
		if file, err := os.Stat(event.Name); err == nil && file.IsDir() {
			err = watchRecursively(event.Name, watcher)
			if err != nil {
				log.Fatal(err)
				exitAll <- 10
			}
		}
		actions = conf.GetActionsOn("create")

	case event.Op&fsnotify.Remove == fsnotify.Remove:
		log.Printf("(%v) [info] Removed file: %s\n", eid, event.Name)
		actions = conf.GetActionsOn("remove")

	case event.Op&fsnotify.Rename == fsnotify.Rename:
		log.Printf("(%v) [info] Renamed file: %s\n", eid, event.Name)
		actions = conf.GetActionsOn("rename")

	case event.Op&fsnotify.Chmod == fsnotify.Chmod:
		log.Printf("(%v) [info] File changed permission: %s\n", eid, event.Name)
		actions = conf.GetActionsOn("chmod")
	}

	for i, action := range actions {
		cid := CID(i + 1)
		task := coodinator.NewTask(
			eid, cid, conf, event, action, exitAll,
		)
		coodinator.notifyNewTask(&task)
		coodinator.addTask(&task)

		log.Printf("(%v/%v) invoking %s ...", task.eid, task.cid, task.action.Name)
		started := task.invoke()
		<-started
	}
}

type EID int64

var currentEID EID

func makeEventID() EID {
	currentEID += 1
	return currentEID
}

type CID int

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
