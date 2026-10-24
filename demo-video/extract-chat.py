#!/usr/bin/env python3
"""Pull one claude.ai chat out of an export and lay it out for the replay.

    python extract-chat.py conversations.json --chat "Statistics topics covered"
    python extract-chat.py one-chat.json                 # a single-chat JSON
    python extract-chat.py conversations.json --list     # what chats are there?

Input is either
  * conversations.json from Settings -> Privacy -> Export data (a list of
    chats), or
  * the JSON body of one chat, saved from the browser's Network tab: the
    response to .../chat_conversations/<uuid>?tree=True&rendering_mode=messages
    (right-click the request -> Copy -> Copy response, paste into a file).
    Copy the RESPONSE only, never the request: its headers carry the cookie.

Writes chat.json (for the renderer) and chat.md (for reading) beside --out.
Every step carries `t`, seconds since the first human message, taken from
the timestamps claude.ai recorded. Nothing is retimed or edited here; the
renderer decides how to show the time, and the real numbers stay in chat.json.

Redaction happens before anything is written, so neither output ever holds
the unredacted text:
  --redact-emails          any email address -> [email]
  --redact-ids             runs of 5+ digits (roll numbers, phones) -> [id]
  --redact WORD[,WORD...]  names or words, case-insensitive -> [redacted]
  --redact-regex RE        anything matching RE -> [redacted] (repeatable)
"""
import argparse
import json
import re
import sys
from datetime import datetime
from pathlib import Path


def parse_time(s):
    if not s:
        return None
    # claude.ai writes 2026-10-21T09:14:03.123456Z; fromisoformat on 3.11+
    # takes the Z, older ones do not.
    return datetime.fromisoformat(s.replace("Z", "+00:00"))


def load_chats(path):
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    if isinstance(data, dict) and "chat_messages" in data:
        return [data]
    if isinstance(data, list):
        return [c for c in data if isinstance(c, dict) and "chat_messages" in c]
    sys.exit(f"{path}: neither a conversations.json nor a single chat")


def pick_chat(chats, name):
    if name is None:
        if len(chats) == 1:
            return chats[0]
        sys.exit(f"{len(chats)} chats in this file; pass --chat NAME (see --list)")
    exact = [c for c in chats if (c.get("name") or "").strip().lower() == name.lower()]
    if len(exact) == 1:
        return exact[0]
    part = [c for c in chats if name.lower() in (c.get("name") or "").lower()]
    if len(part) == 1:
        return part[0]
    found = exact or part
    if not found:
        sys.exit(f"no chat named like {name!r} (see --list)")
    sys.exit("several chats match:\n" + "\n".join(
        f"  {c.get('uuid')}  {c.get('updated_at', '')[:16]}  {c.get('name')}" for c in found))


def main_branch(chat):
    """The messages the claude.ai page shows, in order.

    A chat with an edited or retried message is a tree. The export lists every
    branch; the page shows the path ending at current_leaf_message_uuid. With no
    leaf or no parent links, the stored order is all there is."""
    msgs = chat.get("chat_messages") or []
    leaf = chat.get("current_leaf_message_uuid")
    by_id = {m.get("uuid"): m for m in msgs}
    if not leaf or leaf not in by_id or not any(m.get("parent_message_uuid") for m in msgs):
        if len({m.get("parent_message_uuid") for m in msgs}) < len(msgs):
            print("note: this chat has branches but no current leaf; keeping stored order",
                  file=sys.stderr)
        return msgs
    path, seen, cur = [], set(), leaf
    while cur in by_id and cur not in seen:
        seen.add(cur)
        path.append(by_id[cur])
        cur = by_id[cur].get("parent_message_uuid")
    return list(reversed(path))


def block_text(content):
    """Text out of a tool_result's content, which is a string or a block list."""
    if isinstance(content, str):
        return content
    out = []
    for b in content or []:
        if isinstance(b, dict):
            if b.get("type") == "text":
                out.append(b.get("text", ""))
            elif "text" in b:
                out.append(str(b["text"]))
            else:
                out.append(json.dumps(b, ensure_ascii=False))
    return "\n".join(out)


def steps_of(msg, t0):
    def since(s):
        t = parse_time(s)
        return None if t is None or t0 is None else round((t - t0).total_seconds(), 3)

    blocks = msg.get("content")
    steps = []
    if not blocks:  # older exports carry only a flat text field
        blocks = [{"type": "text", "text": msg.get("text", "")}]
    for b in blocks:
        kind = b.get("type")
        step = {
            "t": since(b.get("start_timestamp")) if b.get("start_timestamp") else since(msg.get("created_at")),
            "t_end": since(b.get("stop_timestamp")),
        }
        if kind == "text":
            step.update(kind="text", text=b.get("text", ""))
        elif kind == "thinking":
            step.update(kind="thinking", text=b.get("thinking", ""))
        elif kind == "tool_use":
            step.update(kind="tool_call", tool=b.get("name"), id=b.get("id"),
                        input=b.get("input") or {}, message=b.get("message"))
        elif kind == "tool_result":
            step.update(kind="tool_result", tool=b.get("name"), id=b.get("tool_use_id"),
                        error=bool(b.get("is_error")), text=block_text(b.get("content")))
        else:
            step.update(kind=kind or "unknown", raw=b)
        steps.append(step)
    return steps


class Redactor:
    def __init__(self, args):
        self.rules = []
        if args.redact_emails:
            self.rules.append((re.compile(r"[\w.+-]+@[\w-]+(\.[\w-]+)+"), "[email]"))
        if args.redact_ids:
            self.rules.append((re.compile(r"(?<!\d)\d{5,}(?!\d)"), "[id]"))
        for word in [w.strip() for w in (args.redact or "").split(",") if w.strip()]:
            self.rules.append((re.compile(re.escape(word), re.I), "[redacted]"))
        for rx in args.redact_regex or []:
            self.rules.append((re.compile(rx), "[redacted]"))
        self.hits = 0

    def __call__(self, value):
        if isinstance(value, str):
            for rx, rep in self.rules:
                value, n = rx.subn(rep, value)
                self.hits += n
            return value
        if isinstance(value, list):
            return [self(v) for v in value]
        if isinstance(value, dict):
            return {k: self(v) for k, v in value.items()}
        return value


def to_markdown(out):
    lines = [f"# {out['name']}", "", f"Started {out['started_at']} (UTC). "
             "Times are seconds since the first message.", ""]
    for m in out["messages"]:
        lines.append(f"## {m['sender']}  ·  {m['t']}s")
        for s in m["steps"]:
            at = f"[{s['t']}s]" if s.get("t") is not None else ""
            if s["kind"] == "text":
                lines += ["", s["text"].strip(), ""]
            elif s["kind"] == "thinking":
                lines += ["", f"> _thinking {at}_ " + s["text"].strip().replace("\n", "\n> "), ""]
            elif s["kind"] == "tool_call":
                lines += ["", f"**tool call {at}** `{s['tool']}` "
                          + json.dumps(s["input"], ensure_ascii=False), ""]
            elif s["kind"] == "tool_result":
                body = s["text"].strip()
                if len(body) > 1500:
                    body = body[:1500] + f"\n… ({len(s['text'])} chars in chat.json)"
                lines += ["", f"**tool result {at}** `{s['tool']}`"
                          + (" (error)" if s["error"] else ""), "", "```", body, "```", ""]
            else:
                lines += ["", f"_{s['kind']} block {at}_", ""]
    return "\n".join(lines) + "\n"


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("input")
    ap.add_argument("--chat", help="chat name, exact or a unique part of it")
    ap.add_argument("--list", action="store_true", help="list the chats and stop")
    ap.add_argument("--out", default=".", help="folder for chat.json and chat.md")
    ap.add_argument("--redact-emails", action="store_true")
    ap.add_argument("--redact-ids", action="store_true")
    ap.add_argument("--redact", help="comma-separated words to blank out")
    ap.add_argument("--redact-regex", action="append")
    args = ap.parse_args()

    chats = load_chats(args.input)
    if args.list:
        for c in sorted(chats, key=lambda c: c.get("updated_at", ""), reverse=True):
            print(f"{c.get('updated_at', '')[:16]}  {len(c.get('chat_messages') or []):3d} msgs  {c.get('name')}")
        return

    chat = pick_chat(chats, args.chat)
    msgs = main_branch(chat)
    first_human = next((m for m in msgs if m.get("sender") == "human"), msgs[0] if msgs else None)
    t0 = parse_time(first_human.get("created_at")) if first_human else None

    out = {
        "name": chat.get("name"),
        "uuid": chat.get("uuid"),
        "started_at": first_human.get("created_at") if first_human else None,
        "model": chat.get("model"),
        "messages": [],
    }
    for m in msgs:
        created = parse_time(m.get("created_at"))
        out["messages"].append({
            "sender": m.get("sender"),
            "t": None if created is None or t0 is None else round((created - t0).total_seconds(), 3),
            "steps": steps_of(m, t0),
            "attachments": [a.get("file_name") for a in (m.get("attachments") or []) + (m.get("files") or [])
                            if isinstance(a, dict)],
        })

    redact = Redactor(args)
    out = redact(out)

    dest = Path(args.out)
    dest.mkdir(parents=True, exist_ok=True)
    (dest / "chat.json").write_text(json.dumps(out, indent=2, ensure_ascii=False), encoding="utf-8")
    (dest / "chat.md").write_text(to_markdown(out), encoding="utf-8")

    n_tools = sum(s["kind"] == "tool_call" for m in out["messages"] for s in m["steps"])
    timed = sum(s.get("t") is not None for m in out["messages"] for s in m["steps"])
    total = sum(len(m["steps"]) for m in out["messages"])
    print(f"{out['name']}: {len(out['messages'])} messages, {n_tools} tool calls, "
          f"{timed}/{total} steps timed, {redact.hits} redactions -> {dest / 'chat.json'}")


if __name__ == "__main__":
    main()
