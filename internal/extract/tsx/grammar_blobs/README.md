# VB.NET grammar tables

`vbnet.bin` is generated from `src/parser.c` in
https://github.com/repowise-dev/tree-sitter-vb-dotnet at tag `v0.3.0`.
The upstream MIT license is retained in `LICENSE.vbnet`.
There is no external scanner. Runtime loading/parsing is pure Go.

Regenerate from the bag root with the existing gotreesitter dependency:

```sh
CGO_ENABLED=0 go run github.com/odvcencio/gotreesitter/cmd/ts2go \
  -input /path/to/tree-sitter-vb-dotnet/src/parser.c \
  -output /path/to/temporary/vbnet_gen.go -package tsx -name vbnet
```

Copy the generated `grammar_blobs/vbnet.bin` here. The generated Go wrapper
is not used; `tsx.Language` loads the embedded blob and propagates errors.
