#!/usr/bin/env python3
"""Check local Markdown links in the entry points and active documentation roots, offline.

evidence/ and other historical records are not scanned; node_modules and the digest-pinned
corpus artifacts/ trees are skipped.
"""

import re
from pathlib import Path
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
# Active documentation only. evidence/ and other historical records are not scanned.
ACTIVE_ROOTS = ("docs", "corpus", "infra", "extensions")


def prose(text):
    """Blank out fenced code blocks, keeping line numbers."""
    return re.sub(r"(?ms)^\s*(`{3,}|~{3,})[^\n]*\n.*?^\s*\1[^\n]*$",
                  lambda match: "\n" * match[0].count("\n"), text)


def code_spans(text):
    """Offsets of inline code spans, paired within a single line. Multi-line spans are rare, and
    pairing across lines let one stray backtick hide links in other blocks, so they are ignored.
    A backslash escapes a backtick only where it would open a span; a closing run is any run
    of equal width."""
    spans = []
    offset = 0
    for line in text.split("\n"):
        runs = [(m.start(), m.end()) for m in re.finditer(r"`+", line)]
        index = 0
        while index < len(runs):
            start, end = runs[index]
            backslashes = len(line[:start]) - len(line[:start].rstrip("\\"))
            close = next((j for j in range(index + 1, len(runs))
                          if runs[j][1] - runs[j][0] == end - start), None)
            if backslashes % 2 or close is None:
                index += 1
                continue
            spans.append((offset + start, offset + runs[close][1]))
            index = close + 1
        offset += len(line) + 1
    return spans


def links(text):
    text = prose(text)
    spans = code_spans(text)

    def inside(offset):
        return any(start <= offset < end for start, end in spans)

    definitions = dict(re.findall(r"(?m)^\s*\[([^\]]+)\]:\s*<?([^\s>]+)>?", text))
    for match in re.finditer(r"\[[^\]\n]*\]\(<?([^\s)>]+)>?(?:\s+[^)]*)?\)", text):
        if not inside(match.start()):
            yield match.start(), match[1]
    for match in re.finditer(r"\[([^\]\n]+)\]\[([^\]\n]*)\]", text):
        if not inside(match.start()):
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
    urls = lambda text: [url for _, url in links(text)]
    assert urls('[a](a.md#part) [b](<b.md> "title")') == ["a.md#part", "b.md"]
    assert urls("Use `[text](url)` and ``[a](b) `x` ``.") == []
    assert urls("`code` then [a](real.md)") == ["real.md"]
    assert urls("[`tool`][guide]\n\n[guide]: missing.md") == ["missing.md"]
    assert urls("\\`[a](x.md) `b`") == ["x.md"]
    assert urls("one ` here\n\n[a](y.md) and ` there") == ["y.md"]
    assert urls("- one `\n- [real](missing.md)\n- two `") == ["missing.md"]
    assert urls("`one \\` [real](missing.md) `two`") == ["missing.md"]
    assert urls("| a ` |\n| [real](missing.md) |\n| b ` |") == ["missing.md"]
    assert urls("# head `\n[real](missing.md)\n## head `") == ["missing.md"]
    assert urls("```sh\n[not a link](missing)\n```") == []
    assert anchors("# A heading\n## A heading\n") == {"a-heading", "a-heading-1"}
    assert anchors("### Terminal client `tbound chat`\n") == {"terminal-client-tbound-chat"}
    contracts = anchors((ROOT / "docs/product-contracts.md").read_text(encoding="utf-8"))
    assert "terminal-client-tbound-chat" in contracts and "terminal-client" not in contracts
    documents = [ROOT / name for name in ("README.md", "USAGE.md", "OVERVIEW.md")]
    for name in ACTIVE_ROOTS:
        documents += sorted(path for path in (ROOT / name).rglob("*.md")
                            if "node_modules" not in path.parts
                            and not (name == "corpus" and "artifacts" in path.parts))
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
