# Grpc-Chat

Just playing with grpc

- Regenerate

```shell
protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative protos/grpc_chat.proto
```

- Run

```shell
go run server/server.go
```

```shell
go run client/client.go
```
