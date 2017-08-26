
NAME := watchevent
SRC := *.go
VERSION := $(shell git describe --tags)
RELEASE_LDFLAGS := $(DEVEL_LDFLAGS) -extldflags '-static'
DEVEL_LDFLAGS := -X main.version=$(VERSION)

all: devel

setup:
	go get github.com/Masterminds/glide

deps: setup
	glide install

update: setup
	glide update

# Make static-liked binary (slow)
release: $(SRC)
	go build -tags netgo -installsuffix netgo -ldflags "$(RELEASE_LDFLAGS)" -o bin/$(NAME)

devel: $(SRC)
	go build -ldflags "$(DEVEL_LDFLAGS)" -o bin/$(NAME)

.PHONY: all setup deps update release devel
