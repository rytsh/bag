# Graphify 0.9.73 regression corpus

`graphify_upstream_golden.json` was generated from Graphify-Labs/graphify
commit `ef4450d` (0.9.73). It compares exact node ID/label/location/file and
edge source/target/relation/confidence tuples, including edge direction saved
in Graphify's `_src`/`_tgt` attributes.

Covered upstream changes:

- Rust, Zig, C++ and Scala 3 enum members (`case_of`).
- Java inherited methods, override precedence, `super` and unknown bases.
- Python module calls when imported functions contain nested functions.
- Kotlin annotations on properties without an explicit type.

Reproduction (run with Graphify 0.9.73 installed, from the bag root):

```python
import json
from pathlib import Path
from graphify.extract import collect_files, extract
from graphify.build import build_from_json

root = Path("testdata/upstream").resolve()
result = extract(collect_files(root), root=root, parallel=False)
graph = build_from_json(result, root=root)
golden = {
    "nodes": sorted([
        [str(n), d.get("label", ""), d.get("source_location", ""),
         d.get("source_file", "")]
        for n, d in graph.nodes(data=True)
    ]),
    "edges": sorted([
        [d.get("_src", str(u)), d.get("_tgt", str(v)),
         d.get("relation", ""), d.get("confidence", "")]
        for u, v, d in graph.edges(data=True)
    ]),
}
Path("testdata/graphify_upstream_golden.json").write_text(
    json.dumps(golden, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
)
```

This is a targeted extraction parity test, not a claim of full Graphify
0.9.73 feature parity. Astro extraction, Solidity free functions and VB.NET
qualified calls still need dedicated extractor support in bag. Python/agent
installation changes and database push changes are outside this port.
