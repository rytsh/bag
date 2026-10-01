# Astro, Solidity and VB.NET parity corpus

`graphify_frameworks_golden.json` was generated with Graphify 0.9.73,
commit `ef4450d`, tree-sitter-solidity 1.2.13 and tree-sitter-vb-dotnet 0.3.0.
The exact tuple comparison covers:

- Astro frontmatter TypeScript, client scripts, scripts on one line,
  quoted `>` attributes, non-JS JSON scripts, template-only components,
  Unicode template text, static imports and template dynamic imports.
- Solidity file-level functions, arity/typed overload IDs, ambiguous calls,
  contract scope, constructors, modifiers, receive/fallback, data members,
  imports, inheritance and using directives.
- VB.NET namespaces, modules, constructors, qualified calls, optional
  arguments, partial types, events/Handles, fields, properties and enums.
  Unknown value receivers are deliberately not resolved.

To regenerate, use the Python snippet in `upstream-parity.md`, substituting
`testdata/frameworks` for the input and
`testdata/graphify_frameworks_golden.json` for the output. All input paths
must be absolute; retain Graphify's `_src`/`_tgt` edge direction attributes.

Graphify uses an undirected graph by default, so opposite calls collapse
into a single edge in the golden. The VB.NET cache regression separately
checks both partial-type calls before graph assembly.

The VB.NET parser tables are embedded and loaded through gotreesitter;
see `internal/extract/tsx/grammar_blobs/README.md` for provenance and regeneration.
Python is needed only to regenerate the reference, never to run bag.
