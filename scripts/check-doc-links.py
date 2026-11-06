#!/usr/bin/env python3
"""Check local Markdown links in the entry points and docs tree, offline."""

import re
from pathlib import Path
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
# Active documentation only. evidence/ and other historical records are not scanned.
ACTIVE_ROOTS = ("docs", "corpus", "infra", "extensions")


def prose(text):
    text = re.sub(r"(?ms)^\s*(`{3,}|~{3,})[^\n]*\n.*?^\s*\1[^\n]*$",
                  lambda match: "\n" * match[0].count("\n"), text)
    # Inline code spans are examples, not links.
    return re.sub(r"(?s)(?<!`)(`+)(?!`)(.+?)(?<!`)\1(?!`)",
                  lambda match: "\n" * match[0].count("\n"), text)


def links(text):
    text = prose(text)
    definitions = dict(re.findall(r"(?m)^\s*\[([^\]]+)\]:\s*<?([^\s>]+)>?", text))
    for match in re.finditer(r"\[[^\]\n]*\]\(<?([^\s)>]+)>?(?:\s+[^)]*)?\)", text):
        yield match.start(), match[1]
    for match in re.finditer(r"\[([^\]\n]+)\]\[([^\]\n]*)\]", text):
        yield match.start(), definitions.get(match[2] or match[1], "MISSING-REFERENCE")


def anchors(text):
    seen = {}
    result = set(re.findall(r'(?:id|name)=[\"\']([^\"\']+)[\"\']', text))
    for heading in re.findall(r"(?m)^#{1,6}\s+(.+?)\s*#*\s*$", prose(text)):
        slug = re.sub(r"[^\w\- ]", "", heading.lower()).replace(" ", "-")
        count = seen.get(slug, 0)
        seen[slug] = count + 1
        result.add(slug + (f"-{count}" if count else ""))
    return result


def main():
    assert [url for _, url in links('[a](a.md#part) [b](<b.md> "title")')] == ["a.md#part", "b.md"]
    assert list(links("Use `[text](url)` and ``[a](b) `x` ``.")) == []
    assert [url for _, url in links("`code` then [a](real.md)")] == ["real.md"]
    assert list(links("```sh\n[not a link](missing)\n```")) == []
    assert anchors("# A heading\n## A heading\n") == {"a-heading", "a-heading-1"}
    documents = [ROOT / name for name in ("README.md", "USAGE.md", "OVERVIEW.md")]
    for name in ACTIVE_ROOTS:
        documents += sorted(path for path in (ROOT / name).rglob("*.md") if "node_modules" not in path.parts)
    checked = 0
    errors = []
    for document in documents:
        text = document.read_text(encoding="utf-8")
        for offset, url in links(text):
            parsed = urlsplit(url)
            if parsed.scheme or parsed.netloc or parsed.path.startswith("/"):
                continue
            checked += 1
            target = document.parent / unquote(parsed.path) if parsed.path else document
            reason = "missing path" if not target.exists() else ""
            if not reason and parsed.fragment and target.suffix == ".md":
                if unquote(parsed.fragment) not in anchors(target.read_text(encoding="utf-8")):
                    reason = "missing anchor"
            if reason:
                line = text[:offset].count("\n") + 1
                errors.append(f"{document.relative_to(ROOT)}:{line}: {url}: {reason}")
    for error in errors:
        print(error)
    print(f"{len(documents)} documents, {checked} local links, {len(errors)} broken")
    return bool(errors)


if __name__ == "__main__":
    raise SystemExit(main())
