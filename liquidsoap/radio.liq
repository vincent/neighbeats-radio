# neighbeats-radio: Liquidsoap only plays. The director decides what is queued.
#
# Stream:  http://HOST:8000/live.mp3        (behind Traefik, basic auth there)
# Control: http://liquidsoap:8005/control/* (internal only, never routed by Traefik)

settings.log.level.set(3)
settings.harbor.bind_addrs.set(["0.0.0.0"])

control_port = 8005

# --- sources -----------------------------------------------------------------

# The director pushes absolute file paths here. Music is mounted at the same path
# Jellyfin uses, so neighbeats' answers are playable as they come.
music = request.queue(id="music")

# Known-good tracks, used only when the music queue is empty.
safe = playlist(mode="randomize", reload_mode="watch", "/fallback")

# Last resort so the chain is never empty: a quiet tone.
ident_loop = amplify(0.05, sine(440.))

radio = fallback(track_sensitive=false, [music, safe, ident_loop])

# --- what is playing ---------------------------------------------------------

track_id = ref(0)
current = ref([("filename", ""), ("artist", ""), ("title", "")])

radio.on_track(fun (m) -> begin
  track_id := !track_id + 1
  current := [("filename", m["filename"]), ("artist", m["artist"]), ("title", m["title"])]
  log("track #{!track_id}: #{m["artist"]} - #{m["title"]} (#{m["filename"]})")
end)

# --- control API -------------------------------------------------------------
# Plain HTTP + JSON, so the Go side needs no telnet client.

def reply_error(response, code, message)
  response.status_code(code)
  response.json({error = message})
end

# GET /control/status
#   {"track_id": 7, "queued": 1, "current": {"filename": ..., "artist": ..., "title": ...}}
# "queued" counts requests waiting behind the one playing.
harbor.http.register(port=control_port, method="GET", "/control/status", fun (_, response) -> begin
  response.json({
    track_id = !track_id,
    queued = list.length(music.queue()),
    current = {
      filename = list.assoc(default="", "filename", !current),
      artist = list.assoc(default="", "artist", !current),
      title = list.assoc(default="", "title", !current)
    }
  })
end)

# POST /control/push   body: an absolute path (raw text, no quoting)
harbor.http.register(port=control_port, method="POST", "/control/push", fun (request, response) -> begin
  path = string.trim(request.body())
  if path == "" or not string.prefix("/", path) then
    reply_error(response, 400, "body must be an absolute path")
  elsif not file.exists(path) then
    reply_error(response, 404, "no such file: #{path}")
  else
    music.push(request.create(path))
    response.json({pushed = path, queued = list.length(music.queue())})
  end
end)

# POST /control/skip
harbor.http.register(port=control_port, method="POST", "/control/skip", fun (_, response) -> begin
  music.skip()
  safe.skip()
  response.json({skipped = true})
end)

# POST /control/flush: drop everything waiting in the music queue (not the current track).
harbor.http.register(port=control_port, method="POST", "/control/flush", fun (_, response) -> begin
  music.set_queue([])
  response.json({queued = 0})
end)

# --- output ------------------------------------------------------------------

output.harbor(id="stream", %mp3(bitrate=192), mount="live.mp3", port=8000, radio)
