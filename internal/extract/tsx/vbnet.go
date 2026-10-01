package tsx

import _ "embed"

// vbnetGrammar contains the MIT-licensed tree-sitter-vb-dotnet v0.3.0
// parse tables, converted by gotreesitter's ts2go. See grammar_blobs/README.md.
//
//go:embed grammar_blobs/vbnet.bin
var vbnetGrammar []byte
