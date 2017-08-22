
NAME := watchevent
SRC := *.go
VERSION := $(shell git describe --tags)
LDFLAGS := -X main.version=$(VERSION)

all: $(NAME)

setup:
	go get github.com/Masterminds/glide

deps: setup
	glide install

update: setup
	glide update

$(NAME): $(SRC)
	go build -ldflags "$(LDFLAGS)" -o bin/$(NAME)

.PHONY: setup deps update
