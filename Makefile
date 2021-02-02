build:
	@go build -o releases/controller -mod vendor

dockerbuild:
	docker build --network host -t maxfailpod_controller .

dockerpush:
	docker push maxfailpod_controller

run:
	@go run -mod=vendor main.go