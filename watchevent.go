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
		err := watchDirsUnder(dir, watcher)
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
	Name  string
	Sleep string
	Run   string
}

func loadConfig(filename string) (*Config, error) {
	buf, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

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
	if action.Sleep == "" {
		return errors.New("action's 'sleep' is empty")
	}
	if _, err := parseSleepMSec(action.Sleep); err != nil {
		return err
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

func invokeAction(actionName string, config *Config, done chan int) {
	action := lookupAction(actionName, config)
	if action == nil {
		log.Println(actionName + ": Can't look up action")
		done <- 20
		return
	}
	// Sleep
	msec := mustParseSleepMSec(action.Sleep)
	time.Sleep(time.Duration(msec) * time.Millisecond)
	// Action
	cmd := config.Shell[0]
	err := exec.Command(cmd, append(config.Shell[1:], action.Run)...).Run()
	if err != nil {
		log.Println(err)
		done <- 21
		return
	}
}

var sleepPattern = regexp.MustCompile(`^(\d+)(m?s(ec)?)$`)

func parseSleepMSec(sleep string) (int, error) {
	result := sleepPattern.FindStringSubmatch(sleep)
	if len(result) == 0 {
		return 0, errors.New(sleep + ": 'sleep' is invalid value")
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

func mustParseSleepMSec(sleep string) int {
	msec, err := parseSleepMSec(sleep)
	if err != nil {
		panic(err)
	}
	return msec
}

func poll(watcher *fsnotify.Watcher, config *Config, done chan int) {
	for {
		select {
		case event := <-watcher.Events:
			log.Println("event: ", event)
			switch {
			case event.Op&fsnotify.Write == fsnotify.Write:
				log.Println("Modified file: ", event.Name)
				if config.OnWrite != "" {
					go invokeAction(config.OnWrite, config, done)
				}
			case event.Op&fsnotify.Create == fsnotify.Create:
				log.Println("Created file: ", event.Name)
				// Watch a new directory
				if file, err := os.Stat(event.Name); err == nil && file.IsDir() {
					err = watcher.Add(event.Name)
					if err != nil {
						log.Fatal(err)
						done <- 10
					}
					log.Println("Watched: ", event.Name)
				}
				if config.OnCreate != "" {
					go invokeAction(config.OnCreate, config, done)
				}
			case event.Op&fsnotify.Remove == fsnotify.Remove:
				log.Println("Removed file: ", event.Name)
				if config.OnRemove != "" {
					go invokeAction(config.OnRemove, config, done)
				}
			case event.Op&fsnotify.Rename == fsnotify.Rename:
				log.Println("Renamed file: ", event.Name)
				if config.OnRename != "" {
					go invokeAction(config.OnRename, config, done)
				}
			case event.Op&fsnotify.Chmod == fsnotify.Chmod:
				log.Println("File changed permission: ", event.Name)
				if config.OnChmod != "" {
					go invokeAction(config.OnChmod, config, done)
				}
			}

		case err := <-watcher.Errors:
			log.Println("error: ", err)
			done <- 11
		}
	}
}

func watchDirsUnder(root string, watcher *fsnotify.Watcher) error {
	files, err := ioutil.ReadDir(root)
	if err != nil {
		return err
	}
	err = watcher.Add(root)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.IsDir() {
			err := watchDirsUnder(filepath.Join(root, file.Name()), watcher)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
