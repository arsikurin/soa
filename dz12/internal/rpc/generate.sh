#!/bin/sh
set -eu
plugin_dir=$(mktemp -d)
trap 'rm -rf "$plugin_dir"' EXIT HUP INT TERM
cat > "$plugin_dir/protoc-gen-go" <<'PLUGIN'
#!/bin/sh
exec go tool protoc-gen-go "$@"
PLUGIN
cat > "$plugin_dir/protoc-gen-go-grpc" <<'PLUGIN'
#!/bin/sh
exec go tool protoc-gen-go-grpc "$@"
PLUGIN
chmod +x "$plugin_dir/protoc-gen-go" "$plugin_dir/protoc-gen-go-grpc"
mkdir -p pb
PATH="$plugin_dir:$PATH" protoc -I ../../proto \
  --go_out=../.. --go_opt=module=marketplace \
  --go-grpc_out=../.. --go-grpc_opt=module=marketplace \
  ../../proto/marketplace/v1/marketplace.proto
