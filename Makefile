build:
	mkdir -p build/
	go build -o build/wolfnode ./cmd/wolfnode
	go build -o build/wolfclient ./cmd/wolfclient

clean:
	rm -rf build/*

test:
	go test ./...
	go vet ./...
