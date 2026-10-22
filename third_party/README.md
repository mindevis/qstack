# Third-party proto definitions

Vendored instead of fetched from the Buf Schema Registry, so that code generation
works offline and does not depend on registry availability. Files are copied
verbatim; do not edit them.

| Path | Source | Pinned commit | License |
|---|---|---|---|
| `proto/google/api/{annotations,http,field_behavior}.proto`, `proto/google/rpc/{status,error_details,code}.proto` | https://github.com/googleapis/googleapis | `cec791da64be8ae9fc9d5402515a144e63f673bf` | Apache-2.0 (`licenses/googleapis-LICENSE`) |
| `proto/buf/validate/validate.proto` | https://github.com/bufbuild/protovalidate (`proto/protovalidate/`, tag v1.2.2) | `4baed765070e580226a0a30bf2a357cc027dc1f7` | Apache-2.0 (`licenses/protovalidate-LICENSE`) |

Go code for these files is not generated here: it comes from
`google.golang.org/genproto/googleapis/{api,rpc}` and
`buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go`.

## Updating

Re-download the files at a new commit, update the table above, then run
`make gen` and `make verify`.
