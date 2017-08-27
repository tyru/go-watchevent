package main

import (
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-fsnotify/fsnotify"
)

type TaskCoodinator struct {
	mutex        sync.RWMutex
	runningTasks []*Task
}

func NewTaskCoodinator() *TaskCoodinator {
	return &TaskCoodinator{
		mutex:        sync.RWMutex{},
		runningTasks: []*Task{},
	}
}

func (coodinator *TaskCoodinator) addTask(task *Task) {
	coodinator.mutex.Lock()
	coodinator.runningTasks = append(coodinator.runningTasks, task)
	coodinator.mutex.Unlock()
}

func (coodinator *TaskCoodinator) notifyNewTask(newInv *Task) {
	coodinator.mutex.RLock()
	defer coodinator.mutex.RUnlock()
	for _, task := range coodinator.runningTasks {
		select {
		case task.newTaskEvents <- newInv:
		default:
		}
	}
}

func (coodinator *TaskCoodinator) NewTask(
	eid EID,
	cid CID,
	conf *Config,
	event *fsnotify.Event,
	action *Action,
	exitAll chan<- int,
) Task {
	done := make(chan *TaskResult, 1)
	go func() {
		result := <-done
		// Delete matched task in running tasks
		coodinator.mutex.Lock()
		for i, task := range coodinator.runningTasks {
			if result.task.eid == task.eid &&
				result.task.cid == task.cid {
				// Delete coodinator.runningTasks[i]
				coodinator.runningTasks = append(
					coodinator.runningTasks[:i],
					coodinator.runningTasks[i+1:]...,
				)
				break
			}
		}
		coodinator.mutex.Unlock()
		// Exit program when exitCode is non-zero
		if result.exitCode != 0 {
			exitAll <- result.exitCode
		}
	}()

	return Task{
		eid:           eid,
		cid:           cid,
		conf:          conf,
		event:         event,
		action:        action,
		newTaskEvents: make(chan *Task),
		done:          done,
	}
}

type Task struct {
	eid           EID
	cid           CID
	conf          *Config
	event         *fsnotify.Event
	action        *Action
	newTaskEvents chan *Task
	done          chan *TaskResult
}

type TaskResult struct {
	exitCode int
	task     *Task
}

// FIXME: race condition of task.newTaskEvents
func (task *Task) invoke() <-chan bool {
	started := make(chan bool, 1) // cap=1 to avoid deadlock
	msec := MustParseIntervalMSec(task.action.Interval)
	go func() {
		if !task.sleep(msec, started) {
			return
		}
		task.execute()
	}()
	return started
}

// Returns true if task.execute() can be called
func (task *Task) sleep(msec int64, started chan<- bool) bool {
	if msec <= 0 {
		started <- true
		return true
	}
	log.Printf("(%v/%v) [info] Sleeping %s ...", task.eid, task.cid, task.action.Interval)
	timeout := time.After(time.Duration(msec) * time.Millisecond)
	started <- true // start watching task.newTaskEvents
	select {
	case <-timeout:
		// Execute action
		return true
	case newInv := <-task.newTaskEvents:
		selfOp := task.event.Op
		newOp := newInv.event.Op
		intervalAction, err := task.action.DetermineIntervalAction(selfOp, newOp, Ignore)
		if err != nil {
			log.Println(err)
			log.Printf("(%v/%v) [error] failed to execute '%v'\n", task.eid, task.cid, task.action.Run)
			task.done <- &TaskResult{
				exitCode: 21,
				task:     task,
			}
		}
		if intervalAction == Ignore {
			log.Printf("(%v/%v) [info] %s: ignored (intercepted by %v/%v)\n",
				task.eid, task.cid, task.action.Name, newInv.eid, newInv.cid)
			select {
			case <-timeout:
			}
			// Execute action
			return true
		} else if intervalAction == Retry {
			log.Printf("(%v/%v) [info] %s: retried (intercepted by %v/%v)\n",
				task.eid, task.cid, task.action.Name, newInv.eid, newInv.cid)
			task.invoke()
		} else if intervalAction == Cancel {
			log.Printf("(%v/%v) [info] %s: canceled (intercepted by %v/%v)\n",
				task.eid, task.cid, task.action.Name, newInv.eid, newInv.cid)
			task.done <- &TaskResult{
				exitCode: 0,
				task:     task,
			}
		}
	}
	return false
}

func (task *Task) execute() {
	log.Printf("(%v/%v) [info] Executing %s ...\n", task.eid, task.cid, task.action.Run)
	exe := task.conf.Shell[0]
	cmd := exec.Command(exe, append(task.conf.Shell[1:], task.action.Run)...)
	cmd.Env = append(os.Environ(),
		"WEV_EVENT="+task.event.Op.String(),
		"WEV_PATH="+task.event.Name)
	out, err := cmd.Output()
	var lines []string
	if len(out) > 0 {
		lines = strings.Split(string(out), "\n")
	}
	for _, line := range lines {
		log.Printf("(%v/%v) [debug] out: %s\n", task.eid, task.cid, line)
	}
	switch e := err.(type) {
	case *exec.ExitError: // exit with non-zero status
		status := e.Sys().(syscall.WaitStatus)
		log.Printf("(%v/%v) [warn] exit with non-zero status %d: %s\n", task.eid, task.cid, status, task.action.Run)
	case nil:
	default:
		log.Printf("(%v/%v) [error] failed to execute '%v'\n", task.eid, task.cid, task.action.Run)
		task.done <- &TaskResult{
			exitCode: 22,
			task:     task,
		}
		return
	}
	task.done <- &TaskResult{
		exitCode: 0,
		task:     task,
	}
}
