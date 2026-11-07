#!/usr/bin/env python3
"""Index factual HTTP contracts in JADX output; never copy credentials/bodies."""
import argparse
import json
import re
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("sources", type=Path)
parser.add_argument("--output", type=Path, default=Path("docs/protocol-catalog.json"))
args = parser.parse_args()
paths_file = args.sources / "com/ub/smartresident/backend/api/RestApiPaths.java"
constants = dict(re.findall(r'public static final String (\w+) = "([^"]*)";', paths_file.read_text()))
roots = [args.sources / p for p in ("com/ub/smartresident", "ru/mts/vdome/resident", "ru/mts/resident/auth")]
contracts = []
for root in roots:
    for source in sorted(root.rglob("*.java")):
        text = source.read_text()
        text = "\n".join(line for line in text.splitlines() if "Metadata(" not in line)
        for match in re.finditer(r'@(GET|POST|PUT|PATCH|DELETE)\(("[^"]*"|RestApiPaths\.\w+)\)\s*(?:@\w+\s*)*Object\s+(\w+)\((.*?);', text, re.S):
            method, expr, name, signature = match.groups()
            route = expr[1:-1] if expr.startswith('"') else constants.get(expr.split(".")[-1], expr)
            params = [{"location": loc.lower(), "name": field} for loc, field in re.findall(r'@(Path|Query|Header)\((?:encoded\s*=\s*true,\s*value\s*=\s*)?"([^"]+)"\)', signature)]
            contracts.append({"method": method, "route": route, "operation": name, "parameters": params, "body": "@Body" in signature, "evidence": str(source.relative_to(args.sources))})
result = {"source": "VDome Android 3.0.4r, ru.mts.vdome.resident", "status": "static extraction; not proof of server availability", "coverage": "Retrofit HTTP annotations in resident modules; excludes runtime-built URLs, SDK services, media and push protocols", "operations": contracts}
args.output.parent.mkdir(parents=True, exist_ok=True)
args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
print(f"Indexed {len(contracts)} operations -> {args.output}")
