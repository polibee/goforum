#!/usr/bin/env python3
"""Wrap static Chinese UI text in {{t "..." $.Lang}} and report phrases.

Usage: python scripts/i18n-wrap.py [--write]
Without --write it only reports the phrases that would be wrapped.
"""
import glob
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TEMPLATES = os.path.join(ROOT, "web", "templates")
CJK = re.compile(r"[\u4e00-\u9fff]")
ATTRS = ("placeholder", "title", "aria-label", "alt")


def normalize(text):
    return " ".join(text.split())


def wrap_text_node(match):
    inner = match.group(1)
    if not CJK.search(inner):
        return match.group(0)
    tokens = re.split(r"(\{\{.*?\}\})", inner, flags=re.S)
    changed = False
    for i, token in enumerate(tokens):
        if token.startswith("{{") or not CJK.search(token):
            continue
        stripped = token.strip()
        if not stripped:
            continue
        key = normalize(stripped)
        leading = token[: len(token) - len(token.lstrip())]
        trailing = token[len(token.rstrip()) :]
        tokens[i] = leading + '{{t ' + json.dumps(key, ensure_ascii=False) + " $.Lang}}" + trailing
        changed = True
    if not changed:
        return match.group(0)
    return ">" + "".join(tokens) + "<"


def wrap_attr(match):
    name, value = match.group(1), match.group(2)
    if "{{" in value or "}}" in value or not CJK.search(value):
        return match.group(0)
    key = normalize(value)
    return name + '="{{t ' + json.dumps(key, ensure_ascii=False) + ' $.Lang}}"'


def process(text):
    text = re.sub(r">([^<>]+)<", wrap_text_node, text)
    attr_re = re.compile(r"\b(" + "|".join(ATTRS) + r')="([^"]*)"')
    return attr_re.sub(wrap_attr, text)


def feature_of(path):
    p = path.replace(os.sep, "/")
    if "/admin/" in p:
        return "admin"
    name = os.path.basename(p)
    if any(x in name for x in ("login", "register", "forgot", "reset", "notice", "install", "profile")):
        return "auth"
    if any(x in name for x in ("pricing", "checkout", "orders", "subscription")):
        return "billing"
    if any(x in name for x in ("points", "invites", "lotteries", "lottery", "bounties")):
        return "gamification"
    if any(x in name for x in ("notifications", "notification_prefs", "messages", "conversation")):
        return "notify"
    if "report" in name:
        return "moderation"
    if any(x in name for x in ("feed", "topic", "composer", "edit_post", "user", "home")):
        return "community"
    return "common"


def main():
    write = "--write" in sys.argv
    groups = {}
    changed = 0
    for path in glob.glob(os.path.join(TEMPLATES, "**", "*.html"), recursive=True):
        original = open(path, encoding="utf-8").read()
        updated = process(original)
        if updated != original:
            changed += 1
            if write:
                open(path, "w", encoding="utf-8").write(updated)
        feature = feature_of(path)
        for m in re.finditer(r'\{\{t ("(?:[^"\\]|\\.)*") \$\.Lang\}\}', updated):
            groups.setdefault(feature, set()).add(json.loads(m.group(1)))
    print("templates changed:", changed)
    for feature in sorted(groups):
        print(feature, len(groups[feature]))
    if "--json" in sys.argv:
        print(json.dumps({k: sorted(v) for k, v in groups.items()}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
