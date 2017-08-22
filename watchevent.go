package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"

	"github.com/go-fsnotify/fsnotify"
)

var version = "x.y.z"

func main() {
	os.Exit(Main())
}

func Main() int {
	if len(os.Args) == 1 {
		log.Println("Usage: go run " + filepath.Base(os.Args[0]) + ".go dir [dir2 ...]")
		return 1
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
		return 2
	}
	defer watcher.Close()

	// Run worker
	done := make(chan int)
	go poll(watcher, done)

	// Watch the specified directory
	for i := 1; i < len(os.Args); i++ {
		if file, err := os.Stat(os.Args[i]); err != nil || !file.IsDir() {
			fmt.Fprintln(os.Stderr, os.Args[i], ": given string does not exist or not a directory")
			return 3
		}
	}
	for i := 1; i < len(os.Args); i++ {
		err := watchDirsUnder(os.Args[i], watcher)
		if err != nil {
			fmt.Fprintln(os.Stderr, os.Args[i], ": Could not watch directory")
			return 4
		}
	}

	return <-done
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
