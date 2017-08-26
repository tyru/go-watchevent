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
	"syscall"
	"time"
	"watchevent/config"

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

type Directories []string

func (dir *Directories) String() string {
	return strings.Join(([]string)(*dir), ",")
}

func (dir *Directories) Set(value string) error {
	*dir = append(*dir, value)
	return nil
}

// FIXME: race condition of task.newTask
func invokeTask(task Task) {
	// Sleep
	msec := config.MustParseIntervalMSec(task.action.Interval)
	if msec > 0 {
		log.Printf("(%v/%v) [info] Sleeping %s ...", task.eid, task.cid, task.action.Interval)
		timeout := time.After(time.Duration(msec) * time.Millisecond)
		select {
		case <-timeout:
			// Execute action
		case newInv := <-task.newTask:
			selfOp := task.event.Op
			newOp := newInv.event.Op
			intervalAction, err := task.action.DetermineIntervalAction(selfOp, newOp, config.Ignore)
			if err != nil {
				log.Println(err)
				log.Printf("(%v/%v) [error] failed to execute '%v'\n", task.eid, task.cid, task.action.Run)
				task.done <- TaskResult{
					exitCode: 21,
					task:     task,
				}
				return
			}
			if intervalAction == config.Ignore {
				log.Printf("(%v/%v) [info] %s: ignored (intercepted by %v/%v)\n",
					task.eid, task.cid, task.action.Name, newInv.eid, newInv.cid)
				select {
				case <-timeout:
				}
				// Execute action
			} else if intervalAction == config.Retry {
				log.Printf("(%v/%v) [info] %s: retried (intercepted by %v/%v)\n",
					task.eid, task.cid, task.action.Name, newInv.eid, newInv.cid)
				invokeTask(task)
				task.done <- TaskResult{
					exitCode: 0,
					task:     task,
				}
				return
			} else if intervalAction == config.Cancel {
				log.Printf("(%v/%v) [info] %s: canceled (intercepted by %v/%v)\n",
					task.eid, task.cid, task.action.Name, newInv.eid, newInv.cid)
				task.done <- TaskResult{
					exitCode: 0,
					task:     task,
				}
				return
			}
		}
	}
	// Action
	log.Printf("(%v/%v) [info] Executing %s ...\n", task.eid, task.cid, task.action.Run)
	exe := task.conf.Shell[0]
	cmd := exec.Command(exe, append(task.conf.Shell[1:], task.action.Run)...)
	cmd.Env = append(os.Environ(),
		"WEV_EVENT="+task.event.Op.String(),
		"WEV_PATH="+task.event.Name)
	err := cmd.Run()
	switch e := err.(type) {
	case *exec.ExitError: // exit with non-zero status
		status := e.Sys().(syscall.WaitStatus)
		log.Printf("(%v/%v) [warn] exit with non-zero status %d: %s\n", task.eid, task.cid, status, task.action.Run)
	case nil:
	default:
		log.Printf("(%v/%v) [error] failed to execute '%v'\n", task.eid, task.cid, task.action.Run)
		task.done <- TaskResult{
			exitCode: 22,
			task:     task,
		}
		return
	}
	task.done <- TaskResult{
		exitCode: 0,
		task:     task,
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

type Task struct {
	eid     EID
	cid     CID
	conf    *config.Config
	event   *fsnotify.Event
	action  *config.Action
	newTask chan Task
	done    chan TaskResult
}

type TaskResult struct {
	exitCode int
	task     Task
}

var runningMutex sync.RWMutex = sync.RWMutex{}
var runningTasks []Task

func NewTask(
	eid EID,
	cid CID,
	conf *config.Config,
	event *fsnotify.Event,
	action *config.Action,
	exitAll chan<- int) Task {

	done := make(chan TaskResult)
	go func() {
		result := <-done
		// Delete matched task in runningTasks
		runningMutex.Lock()
		for i, task := range runningTasks {
			if result.task.eid == task.eid &&
				result.task.cid == task.cid {
				// Delete runningTasks[i]
				runningTasks = append(runningTasks[:i], runningTasks[i+1:]...)
				break
			}
		}
		runningMutex.Unlock()
		// Exit program when exitCode is non-zero
		if result.exitCode != 0 {
			exitAll <- result.exitCode
		}
	}()

	return Task{
		eid:     eid,
		cid:     cid,
		conf:    conf,
		event:   event,
		action:  action,
		newTask: make(chan Task),
		done:    done,
	}
}

func handleEvent(event *fsnotify.Event, watcher *fsnotify.Watcher, conf *config.Config, exitAll chan<- int) {
	eid := makeEventID()
	var actions []*config.Action

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
		task := NewTask(eid, cid, conf, event, action, exitAll)
		notifyNewTask(task)

		runningMutex.Lock()
		runningTasks = append(runningTasks, task)
		runningMutex.Unlock()

		log.Printf("(%v/%v) invoking %s ...", task.eid, task.cid, task.action.Name)
		go invokeTask(task)
	}
}

func notifyNewTask(newInv Task) {
	runningMutex.RLock()
	defer runningMutex.RUnlock()
	for _, task := range runningTasks {
		select {
		case task.newTask <- newInv:
		default:
		}
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
