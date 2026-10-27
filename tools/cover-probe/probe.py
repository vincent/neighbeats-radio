#!/usr/bin/env python3
"""Show what a player gets from the MP3 stream: ICY titles and ID3v2 tags (with cover size).

    probe.py [--url URL] [--seconds N] [--stop-after K] [--save-cover DIR]

Prints one line per ICY title or ID3 tag found, with the time since connect. Stdlib only.
Needs a track change to see anything: the server sends metadata only when it changes.
"""
import argparse
import os
import sys
import time
import urllib.request

ID3_SYNCSAFE = 0x7F


def syncsafe(b):
    return (b[0] << 21) | (b[1] << 14) | (b[2] << 7) | b[3]


def parse_id3(buf):
    """Return (frames, total_size) for a buffer starting with a valid ID3v2.3/2.4 header, else None."""
    if len(buf) < 10 or buf[:3] != b"ID3" or buf[3] not in (3, 4) or buf[4] != 0:
        return None
    if any(b > ID3_SYNCSAFE for b in buf[6:10]):
        return None
    size = syncsafe(buf[6:10])
    frames, pos, end = [], 10, 10 + size
    while pos + 10 <= min(end, len(buf)) and buf[pos]:
        fid = buf[pos : pos + 4].decode("latin-1")
        n = syncsafe(buf[pos + 4 : pos + 8]) if buf[3] == 4 else int.from_bytes(buf[pos + 4 : pos + 8], "big")
        frames.append((fid, buf[pos + 10 : pos + 10 + n]))
        pos += 10 + n
    return frames, end


def describe(fid, body):
    if fid == "APIC":
        enc = body[0]
        mime_end = body.index(b"\0", 1)
        mime = body[1:mime_end].decode("latin-1")
        # skip: type byte, then null-terminated description (1 or 2 byte terminator)
        term = b"\0\0" if enc in (1, 2) else b"\0"
        desc_end = body.index(term, mime_end + 2)
        data = body[desc_end + len(term) :]
        return f"APIC {mime} {len(data)} bytes", data
    if fid.startswith("T"):
        return f"{fid} {body[1:].decode('utf-8', 'replace').strip(chr(0))!r}", None
    return f"{fid} {len(body)} bytes", None


def read_exact(resp, n):
    """HTTPResponse.read(n) may return fewer bytes (TLS, chunked); a short read would shift the ICY framing."""
    out = b""
    while len(out) < n:
        more = resp.read(n - len(out))
        if not more:
            raise EOFError("stream closed")
        out += more
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="https://soap.tat.wine/live.mp3")
    ap.add_argument("--seconds", type=int, default=600)
    ap.add_argument("--stop-after", type=int, default=0, help="stop after K metadata events (0 = run to --seconds)")
    ap.add_argument("--save-cover", metavar="DIR")
    ap.add_argument("--no-icy", action="store_true", help="do not ask for ICY titles (in-stream ID3 only; ICY bytes would split a big tag)")
    a = ap.parse_args()

    req = urllib.request.Request(a.url, headers={"User-Agent": "AndroidXMedia3/1.11.1", **({} if a.no_icy else {"Icy-MetaData": "1"})})
    resp = urllib.request.urlopen(req, timeout=120)
    metaint = int(resp.headers.get("icy-metaint", 0))
    print(f"connected: icy-metaint={metaint or 'none'}", flush=True)

    t0, events, beat = time.time(), 0, time.time()
    audio = b""  # recent audio bytes, to find an ID3 tag that starts inside it
    while time.time() - t0 < a.seconds:
        try:
            chunk = read_exact(resp, metaint or 8192)
        except (EOFError, TimeoutError) as e:
            print(f"{time.time() - t0:6.0f}s stream lost: {e or 'stream closed'}", flush=True)
            break
        audio = audio[-9:] + chunk
        i = audio.find(b"ID3")
        while i != -1:
            head = audio[i : i + 10]
            if len(head) == 10 and parse_id3(head + b"\0") is not None:
                need = 10 + syncsafe(head[6:10])
                body = audio[i:]
                while len(body) < need:  # the tag is bigger than one chunk
                    body += read_exact(resp, need - len(body))
                events += 1
                frames, size = parse_id3(body)
                print(f"{time.time() - t0:6.0f}s ID3v2 tag, {size} bytes (in-stream)", flush=True)
                for fid, data in frames:
                    text, pic = describe(fid, data)
                    print(f"         {text}", flush=True)
                    if pic and a.save_cover:
                        os.makedirs(a.save_cover, exist_ok=True)
                        with open(os.path.join(a.save_cover, f"cover-{events}.bin"), "wb") as f:
                            f.write(pic)
                audio = b""
                break
            i = audio.find(b"ID3", i + 1)
        if metaint:
            n = read_exact(resp, 1)[0] * 16
            if n:
                events += 1
                title = read_exact(resp, n).rstrip(b"\0").decode("utf-8", "replace")
                print(f"{time.time() - t0:6.0f}s ICY {title}", flush=True)
        if time.time() - beat >= 60:
            beat = time.time()
            print(f"{time.time() - t0:6.0f}s ... alive, no metadata change yet", flush=True)
        if a.stop_after and events >= a.stop_after:
            break
    print(f"done after {time.time() - t0:.0f}s, {events} metadata events", flush=True)
    return 0 if events else 1


if __name__ == "__main__":
    sys.exit(main())
