
NAME := watchevent
SRC := *.go
VERSION := $(shell git describe --tags)
DEVEL_LDFLAGS := -X main.version=$(VERSION)
RELEASE_LDFLAGS := $(DEVEL_LDFLAGS) -extldflags '-static'
RELEASE_OS := linux windows darwin
RELEASE_ARCH := amd64 386

all: devel

setup:
	go get github.com/Masterminds/glide

deps: setup
	glide install

update: setup
	glide update

# Make static-linked binaries and tarballs
release: $(SRC)
	@for os in $(RELEASE_OS); do \
		for arch in $(RELEASE_ARCH); do \
			if [ $$os = windows ]; then \
				echo "Making zip for ... os=$$os, arch=$$arch"; \
				GOOS=$$os GOARCH=$$arch go build -tags netgo -installsuffix netgo -ldflags "$(RELEASE_LDFLAGS)" -o dist/$(NAME)-$(VERSION)-$$os-$$arch/$(NAME).exe; \
				(cd dist && zip -qr $(NAME)-$(VERSION)-$$os-$$arch.zip $(NAME)-$(VERSION)-$$os-$$arch); \
			else \
				echo "Making tarball for ... os=$$os, arch=$$arch"; \
				GOOS=$$os GOARCH=$$arch go build -tags netgo -installsuffix netgo -ldflags "$(RELEASE_LDFLAGS)" -o dist/$(NAME)-$(VERSION)-$$os-$$arch/$(NAME); \
				strip dist/$(NAME)-$(VERSION)-$$os-$$arch/$(NAME) 2>/dev/null; \
				(cd dist && tar czf $(NAME)-$(VERSION)-$$os-$$arch.tar.gz $(NAME)-$(VERSION)-$$os-$$arch); \
			fi; \
			rm -r dist/$(NAME)-$(VERSION)-$$os-$$arch; \
		done; \
	done

devel: $(SRC)
	go build -ldflags "$(DEVEL_LDFLAGS)" -o bin/$(NAME)

.PHONY: all setup deps update release devel
