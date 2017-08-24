package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v2"

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
	config, err := loadConfig(configFile)
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
	go poll(watcher, config, done)

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

type Config struct {
	OnWrite  string `yaml:"on_write"`
	OnCreate string `yaml:"on_create"`
	OnRemove string `yaml:"on_remove"`
	OnRename string `yaml:"on_rename"`
	OnChmod  string `yaml:"on_chmod"`
	Action   []Action
	Shell    []string
}

type Action struct {
	Name            string
	Interval        string
	EventAtInterval string `yaml:"event_at_interval"`
	Run             string
}

func loadConfig(filename string) (*Config, error) {
	buf, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	// Default values
	var config Config
	if runtime.GOOS == "windows" {
		config.Shell = []string{"cmd.exe", "/c"}
	} else {
		config.Shell = []string{"bash", "-c"}
	}

	err = yaml.Unmarshal(buf, &config)
	if err != nil {
		return nil, err
	}

	err = validateConfig(&config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

func validateConfig(config *Config) error {
	if len(config.Action) == 0 {
		return errors.New("No actions were defined")
	}
	for _, action := range config.Action {
		if err := validateActionConfig(&action); err != nil {
			return err
		}
	}

	if config.OnWrite == "" &&
		config.OnCreate == "" &&
		config.OnRemove == "" &&
		config.OnRename == "" &&
		config.OnChmod == "" {
		return errors.New("No event(s) were specified")
	}

	if config.OnWrite != "" && lookupAction(config.OnWrite, config) == nil {
		return errors.New("Action '" + config.OnWrite + "' is not defined")
	}
	if config.OnCreate != "" && lookupAction(config.OnCreate, config) == nil {
		return errors.New("Action '" + config.OnCreate + "' is not defined")
	}
	if config.OnRemove != "" && lookupAction(config.OnRemove, config) == nil {
		return errors.New("Action '" + config.OnRemove + "' is not defined")
	}
	if config.OnRename != "" && lookupAction(config.OnRename, config) == nil {
		return errors.New("Action '" + config.OnRename + "' is not defined")
	}
	if config.OnChmod != "" && lookupAction(config.OnChmod, config) == nil {
		return errors.New("Action '" + config.OnChmod + "' is not defined")
	}

	return nil
}

func validateActionConfig(action *Action) error {
	if action.Name == "" {
		return errors.New("action's 'name' is empty")
	}

	if action.Interval == "" {
		return errors.New("action's 'interval' is empty")
	}
	if _, err := parseIntervalMSec(action.Interval); err != nil {
		return err
	}

	if action.EventAtInterval != "ignore" &&
		action.EventAtInterval != "cancel" &&
		action.EventAtInterval != "retry" {
		return errors.New("action's 'event_at_interval' is invalid value " +
			"(\"ignore\" or \"cancel\" or \"retry\")")
	}

	if action.Run == "" {
		return errors.New("action's 'run' is empty")
	}
	return nil
}

func lookupAction(actionName string, config *Config) *Action {
	if actionName == "" {
		return nil
	}
	for _, action := range config.Action {
		if action.Name == actionName {
			return &action
		}
	}
	return nil
}

func invokeAction(cid CID,
	actionName string,
	config *Config,
	event *fsnotify.Event,
	newEvent chan bool,
	done chan int) {

	action := lookupAction(actionName, config)
	if action == nil {
		log.Printf("(%v) %s: Can't look up action", cid, actionName)
		done <- 20
		return
	}
	// Sleep
	log.Printf("(%v) Sleeping %s ...", cid, action.Interval)
	msec := mustParseIntervalMSec(action.Interval)
	timeout := time.After(time.Duration(msec) * time.Millisecond)
	if action.EventAtInterval == "ignore" {
		<-timeout
	} else {
		select {
		case <-timeout:
			// Execute action
		case <-newEvent:
			if action.EventAtInterval == "retry" {
				log.Printf("(%v) %s: retried", cid, actionName)
				invokeAction(cid, actionName, config, event, newEvent, done)
				return
			} else if action.EventAtInterval == "cancel" {
				log.Printf("(%v) %s: canceled", cid, actionName)
				return
			}
		}
	}
	// Action
	log.Printf("(%v) Executing %s ...", cid, action.Run)
	exe := config.Shell[0]
	cmd := exec.Command(exe, append(config.Shell[1:], action.Run)...)
	cmd.Env = append(os.Environ(),
		"WEV_EVENT="+event.Op.String(),
		"WEV_PATH="+event.Name)
	err := cmd.Run()
	if err != nil {
		log.Printf("(%v) %v ...", cid, action.Run)
		done <- 21
		return
	}
}

var intervalPattern = regexp.MustCompile(`^(\d+)(m?s(ec)?)$`)

func parseIntervalMSec(interval string) (int, error) {
	result := intervalPattern.FindStringSubmatch(interval)
	if len(result) == 0 {
		return 0, errors.New(interval + ": 'interval' is invalid value")
	}
	msec, err := strconv.Atoi(result[1])
	if err != nil {
		return 0, err
	}
	if result[2] == "sec" || result[2] == "s" {
		msec = msec * 1000
	}
	return msec, nil
}

func mustParseIntervalMSec(interval string) int {
	msec, err := parseIntervalMSec(interval)
	if err != nil {
		panic(err)
	}
	return msec
}

func poll(watcher *fsnotify.Watcher, config *Config, done chan int) {
	for {
		newEvent := make(chan bool, 1)
		select {
		case event := <-watcher.Events:
			fireEvent(&event, newEvent, watcher, config, done)

		case err := <-watcher.Errors:
			log.Println("error: ", err)
			done <- 11
		}
	}
}

func fireEvent(event *fsnotify.Event, newEvent chan bool, watcher *fsnotify.Watcher, config *Config, done chan int) {
	// Do not block when sending to channel
	select {
	case newEvent <- true:
	}

	cid := makeCommandID()

	switch {
	case event.Op&fsnotify.Write == fsnotify.Write:
		log.Printf("(%v) Modified file: %s\n", cid, event.Name)
		if config.OnWrite != "" {
			go invokeAction(cid, config.OnWrite, config, event, newEvent, done)
		}
	case event.Op&fsnotify.Create == fsnotify.Create:
		log.Printf("(%v) Created file: %s\n", cid, event.Name)
		// Watch a new directory
		if file, err := os.Stat(event.Name); err == nil && file.IsDir() {
			err = watchRecursively(event.Name, watcher)
			if err != nil {
				log.Fatal(err)
				done <- 10
			}
		}
		if config.OnCreate != "" {
			go invokeAction(cid, config.OnCreate, config, event, newEvent, done)
		}
	case event.Op&fsnotify.Remove == fsnotify.Remove:
		log.Printf("(%v) Removed file: %s\n", cid, event.Name)
		if config.OnRemove != "" {
			go invokeAction(cid, config.OnRemove, config, event, newEvent, done)
		}
	case event.Op&fsnotify.Rename == fsnotify.Rename:
		log.Printf("(%v) Renamed file: %s\n", cid, event.Name)
		if config.OnRename != "" {
			go invokeAction(cid, config.OnRename, config, event, newEvent, done)
		}
	case event.Op&fsnotify.Chmod == fsnotify.Chmod:
		log.Printf("(%v) File changed permission: %s\n", cid, event.Name)
		if config.OnChmod != "" {
			go invokeAction(cid, config.OnChmod, config, event, newEvent, done)
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
