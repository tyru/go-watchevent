package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"

	"github.com/go-fsnotify/fsnotify"
	"github.com/soh335/sliceflag"
)

var version = "x.y.z"

func main() {
	os.Exit(Main())
}

func Main() int {
	if len(os.Args) == 1 {
		// TODO: -help
		log.Println("Usage: go run " + filepath.Base(os.Args[0]) + ".go dir OPTIONS")
		return 1
	}

	var configFile string
	flag.StringVar(&configFile, "config", "", "config file")
	flag.StringVar(&configFile, "c", "", "config file")
	// TODO
	// var directories = sliceflag.String(flag.CommandLine, "directory", []string{}, "directory to be watched")
	var directories = sliceflag.String(flag.CommandLine, "d", []string{}, "directory to be watched")
	flag.Parse()

	if configFile == "" {
		fmt.Fprintln(os.Stderr, "-config option was not specified")
		return 2
	}
	config, err := loadConfig(configFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, configFile, ": Could not load config file")
		return 3
	}
	fmt.Println("[DEBUG] Config:", config)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not initialize watcher")
		return 4
	}
	defer watcher.Close()

	// Run worker
	done := make(chan int)
	go poll(watcher, done)

	// Watch the specified directory
	for _, dir := range *directories {
		fmt.Println("[DEBUG] Dir: ", dir)
		if file, err := os.Stat(dir); err != nil || !file.IsDir() {
			fmt.Fprintln(os.Stderr, dir, ": given string does not exist or not a directory")
			return 5
		}
	}
	for _, dir := range *directories {
		err := watchDirsUnder(dir, watcher)
		if err != nil {
			fmt.Fprintln(os.Stderr, dir, ": Could not watch directory")
			return 6
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
