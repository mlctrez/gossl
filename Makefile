
NAME=gossl
HOST=optiplex

build: main.go
	mkdir -p temp
	go build -o temp/$(NAME) main.go

copy: build
	scp temp/$(NAME) $(HOST):/tmp/$(NAME)

deploy: copy
	ssh $(HOST) sudo /tmp/$(NAME) -action deploy
