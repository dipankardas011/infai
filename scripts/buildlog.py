#!/usr/bin/env python3
"""Extend the site's buildlog.json with the infaiw build-in-public posts on X.

Each daily post quote-tweets the previous one, so the log is extended by walking
that quote chain backward from the newest post until it reaches a post the log
already has. Release markers are rebuilt from the infaiw-* git tags every run.

The site lives at the root of the gh-pages branch. The buildlog workflow on main
checks that branch out, runs this against its buildlog.json, and commits the result
back to gh-pages; main never carries the file.

Usage (LOG is the site checkout's buildlog.json):
  python3 scripts/buildlog.py --log LOG               # newest "Day NN" post on the profile
  python3 scripts/buildlog.py --log LOG <post url>    # start the walk from a specific post
  python3 scripts/buildlog.py --log LOG --releases    # only refresh release markers

Release markers come from the infaiw-* tags of the repository this script lives in.

Dates use the local timezone; the workflow pins TZ to match the author's.
Posts are read through the public api.fxtwitter.com mirror (X's own read API is
paid). Entry text is the post's first meaningful line; existing entries are never
rewritten.
"""

import argparse
import datetime as dt
import json
import pathlib
import re
import subprocess
import sys
import time
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
API = "https://api.fxtwitter.com"
DAY_RE = re.compile(r"Day\s+(\d+(?:-\d+)?)\s*(\[update\])?\s*(?:of\s+)?", re.I)


def fetch(path):
    req = urllib.request.Request(f"{API}{path}", headers={"User-Agent": "infai-buildlog"})
    with urllib.request.urlopen(req, timeout=20) as resp:
        return json.load(resp)


def post_id(url):
    m = re.search(r"/status/(\d+)", url)
    if not m:
        sys.exit(f"not a post url: {url}")
    return m.group(1)


def strip_prefix(text):
    return re.sub(r"^\[self-documenting\]\s*", "", text.strip())


def latest_day_post(handle):
    # The mirror rejects a ?count= parameter; the default page holds 20 posts.
    for post in fetch(f"/2/profile/{handle}/statuses")["results"]:
        if DAY_RE.match(strip_prefix(post["text"])):
            return post["id"]
    print("no 'Day NN' post in the recent timeline; pass the post url to start from one")
    return None


def to_entry(tweet, handle):
    text = strip_prefix(tweet["text"])
    m = DAY_RE.match(text)
    if not m:
        return None
    paras = [p.replace("\n", " ").strip() for p in re.split(r"\n\s*\n", text[m.end():]) if p.strip()]
    if not paras:
        paras = [""]
    # "Day 03 of building infai harness\n\n<what happened>" keeps the news in paragraph two.
    first = paras[1] if paras[0].lower().startswith("building infai") and len(paras) > 1 else paras[0]
    when = dt.datetime.strptime(tweet["created_at"], "%a %b %d %H:%M:%S %z %Y").astimezone()
    entry = {"day": m.group(1), "date": when.date().isoformat(), "text": first,
             "url": f"https://x.com/{handle}/status/{tweet['id']}"}
    if m.group(2):
        entry["update"] = True
    return entry


def walk(start, known, handle):
    """Follow quote (or self-reply) links back from start until a known post."""
    found, cur = [], start
    while cur and cur not in known:
        tweet = fetch(f"/{handle}/status/{cur}")["tweet"]
        entry = to_entry(tweet, handle)
        if entry:
            found.append(entry)
            print(f"  + day {entry['day']:<6} {entry['date']}  {entry['text'][:70]}")
        quoted = (tweet.get("quote") or {}).get("id")
        replied = tweet.get("replying_to_status") if (tweet.get("replying_to") or "").lower() == handle.lower() else None
        cur = quoted or replied
        time.sleep(0.5)
    return found


def releases():
    out = subprocess.run(
        ["git", "for-each-ref", "--sort=creatordate", "--format=%(refname:short) %(creatordate:iso-strict)", "refs/tags/infaiw-*"],
        cwd=ROOT, check=True, capture_output=True, text=True).stdout
    tags = []
    for line in out.splitlines():
        name, when = line.split()
        tags.append({"tag": name.removeprefix("infaiw-"),
                     "date": dt.datetime.fromisoformat(when).astimezone().date().isoformat()})
    return tags


def main():
    parser = argparse.ArgumentParser(description="Extend the site's buildlog.json.")
    parser.add_argument("--log", required=True, type=pathlib.Path, help="path to the site's buildlog.json")
    parser.add_argument("--releases", action="store_true", help="only refresh release markers")
    parser.add_argument("post", nargs="?", help="post url to start the walk from")
    args = parser.parse_args()

    log = json.loads(args.log.read_text()) if args.log.exists() else {"handle": "DipankarDas011", "entries": []}
    handle = log["handle"]
    if not args.releases:
        known = {post_id(e["url"]) for e in log["entries"]}
        start = post_id(args.post) if args.post else latest_day_post(handle)
        found = walk(start, known, handle)
        log["entries"] = sorted(log["entries"] + found, key=lambda e: int(post_id(e["url"])))
        print(f"{len(found)} new post(s), {len(log['entries'])} total")
    log["releases"] = releases()
    args.log.write_text(json.dumps(log, indent=2, ensure_ascii=False) + "\n")


if __name__ == "__main__":
    main()
