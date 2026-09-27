#!/usr/bin/env python3
"""Compare a bag graph.json against a Graphify graph.json, per language.

Usage: compare.py graphify.json bag.json [--show EXT]
"""
import collections
import json
import sys


def load(p):
    d = json.load(open(p))
    return d["nodes"], d.get("links", d.get("edges", []))


def ext(sf):
    if not sf:
        return "-"
    name = sf.rsplit("/", 1)[-1]
    return name.rsplit(".", 1)[-1] if "." in name else "-"


def nkey(n):
    return (n["id"], n["label"], n.get("source_location") or "")


def ekey(e):
    return (e["source"], e["target"], e["relation"])


def main():
    gn, ge = load(sys.argv[1])
    bn, be = load(sys.argv[2])
    show = sys.argv[4] if len(sys.argv) > 3 and sys.argv[3] == "--show" else None

    by = collections.defaultdict(lambda: [set(), set(), set(), set()])
    for n in gn:
        by[ext(n["source_file"])][0].add(nkey(n))
    for n in bn:
        by[ext(n["source_file"])][1].add(nkey(n))
    for e in ge:
        by[ext(e["source_file"])][2].add(ekey(e))
    for e in be:
        by[ext(e["source_file"])][3].add(ekey(e))

    print(f"{'ext':8} {'nodes g/b/common':>20} {'edges g/b/common':>20}")
    for k in sorted(by):
        a, b, c, d = by[k]
        print(f"{k:8} {len(a):6}/{len(b):5}/{len(a & b):5}   {len(c):6}/{len(d):5}/{len(c & d):5}")
        if show and (show == k or show == "all"):
            for x in sorted(a - b):
                print("   -N", x)
            for x in sorted(b - a):
                print("   +N", x)
            for x in sorted(c - d):
                print("   -E", x)
            for x in sorted(d - c):
                print("   +E", x)


if __name__ == "__main__":
    main()
