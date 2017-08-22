package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

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
	_, err := loadConfig(configFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[error]", configFile, ": Could not load config file")
		return 4
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintln(os.Stderr, "[error] Could not initialize watcher")
		return 5
	}
	defer watcher.Close()

	// Run watcher
	done := make(chan int)
	go poll(watcher, done)

	// Watch the specified directory
	for _, dir := range directories {
		if file, err := os.Stat(dir); err != nil || !file.IsDir() {
			fmt.Fprintln(os.Stderr, "[error]", dir, ": given path does not exist or not a directory")
			return 6
		}
	}
	for _, dir := range directories {
		err := watchDirsUnder(dir, watcher)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[error]", dir, ": Could not watch directory")
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

	// TODO

	return nil
}

func poll(watcher *fsnotify.Watcher, done chan int) {
	for {
		select {
		case event := <-watcher.Events:
			log.Println("event: ", event)
			switch {
			case event.Op&fsnotify.Write == fsnotify.Write:
				log.Println("Modified file: ", event.Name)
			case event.Op&fsnotify.Create == fsnotify.Create:
				log.Println("Created file: ", event.Name)
				if file, err := os.Stat(event.Name); err == nil && file.IsDir() {
					err = watcher.Add(event.Name)
					if err != nil {
						log.Fatal(err)
						done <- 10
					}
					log.Println("Watched: ", event.Name)
				}
			case event.Op&fsnotify.Remove == fsnotify.Remove:
				log.Println("Removed file: ", event.Name)
			case event.Op&fsnotify.Rename == fsnotify.Rename:
				log.Println("Renamed file: ", event.Name)
			case event.Op&fsnotify.Chmod == fsnotify.Chmod:
				log.Println("File changed permission: ", event.Name)
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
