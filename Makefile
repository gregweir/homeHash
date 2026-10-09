.PHONY: build run dev test check clean install uninstall fmt vet systemd

BINARY_NAME=homeHash
INSTALL_PATH=/usr/local/bin
SYSTEMD_PATH=/etc/systemd/system

build:
	go build -o $(BINARY_NAME) cmd/homeHash/main.go

run: build
	./$(BINARY_NAME)

dev:
	go run cmd/homeHash/main.go

test:
	go test ./...

clean:
	rm -f $(BINARY_NAME)
	rm -f homehash.db

install: build
	sudo cp $(BINARY_NAME) $(INSTALL_PATH)/
	sudo mkdir -p /var/lib/homehash
	sudo cp systemd/homeHash.service $(SYSTEMD_PATH)/
	sudo systemctl daemon-reload
	sudo systemctl enable homeHash

uninstall:
	sudo systemctl stop homeHash || true
	sudo systemctl disable homeHash || true
	sudo rm -f $(INSTALL_PATH)/$(BINARY_NAME)
	sudo rm -f $(SYSTEMD_PATH)/homeHash.service
	sudo systemctl daemon-reload

fmt:
	go fmt ./...

vet:
	go vet ./...

check:
	go build ./...
	go vet ./...
	test -z "$$(gofmt -l .)" || { echo 'gofmt needed:'; gofmt -l .; exit 1; }
	go test ./...
