
NAME=gossl
HOST=dune

build:
	mkdir -p temp
	CGO_ENABLED=0 go build -o temp/$(NAME) .

copy: build
	scp temp/$(NAME) $(HOST):/tmp/$(NAME)

sysconfig:
	ssh $(HOST) 'sudo tee /etc/sysconfig/gossl > /dev/null' < .env

deploy: copy sysconfig
	ssh $(HOST) sudo /tmp/$(NAME) -action deploy
