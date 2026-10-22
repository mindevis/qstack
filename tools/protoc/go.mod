// Pinned protobuf/gRPC code generators, run as:
//   go tool -modfile=tools/protoc/go.mod protoc-gen-go
// Isolated: the generator version decides which google.golang.org/protobuf
// runtime the main module needs, so only stable releases are allowed here.
module github.com/mindevis/qstack/tools/protoc

go 1.26

tool (
	google.golang.org/grpc/cmd/protoc-gen-go-grpc
	google.golang.org/protobuf/cmd/protoc-gen-go
)

require (
	google.golang.org/grpc/cmd/protoc-gen-go-grpc v1.6.2 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
