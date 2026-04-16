
NAME=gossl
HOST=optiplex

build:
	mkdir -p temp
	CGO_ENABLED=0 go build -o temp/$(NAME) .

copy: build
	scp temp/$(NAME) $(HOST):/tmp/$(NAME)

deploy: copy
	ssh $(HOST) sudo /tmp/$(NAME) -action deploy
