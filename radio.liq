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

      # --- transitions (R2) --------------------------------------------------------

      # Trim silence at the head and tail of tracks.
      music_trimmed = blank.eat(threshold=-50., music)

      # ReplayGain tags where present; untagged files play as they are.
      enable_replaygain_metadata()
      music_leveled = replaygain(music_trimmed)

      # Crossfade on music only. The director sets liq_cross_duration (with liq_fade_out
      # and liq_fade_in) on a track when it is pushed, from the hop that follows it:
      # a close hop blends long, a distant one gets a short fade. 5 s when unset.
      music_x = crossfade(duration=5., fade_in=3., fade_out=3., music_leveled)

      # Known-good tracks, used only when the music queue is empty.
      safe = playlist(mode="randomize", reload_mode="watch", "/fallback")

      # Last resort so the chain is never empty: a quiet tone.
      ident_loop = amplify(0.05, sine(440.))

      on_air = fallback(track_sensitive=false, [music_x, safe, ident_loop])

      # --- what is playing ---------------------------------------------------------
      # Counted on the music chain, before the voice is mixed in: a spoken break is not a track.

      track_id = ref(0)
      current = ref([("filename", ""), ("artist", ""), ("title", "")])

      # Cover art: a picture file next to the track, unless the file carries its own.
      # Too big a picture is skipped, it is re-sent with the metadata.
      cover_names = [
        ("folder.jpg", "image/jpeg"), ("cover.jpg", "image/jpeg"), ("front.jpg", "image/jpeg"),
        ("folder.png", "image/png"), ("cover.png", "image/png")
      ]
      cover_max_bytes = 150000
      cover = ref([])
      resend = ref(0)

      def find_cover(filename)
        dir = path.dirname(filename)
        hits = list.filter(fun (c) -> file.exists("#{dir}/#{fst(c)}"), cover_names)
        if hits == [] then
          []
        else
          hit = list.hd(hits)
          data = file.contents("#{dir}/#{fst(hit)}")
          if string.length(data) > cover_max_bytes then [] else [("coverart", data), ("coverart_mime", snd(hit))] end
        end
      end

      on_air.on_track(synchronous=false, fun (m) -> begin
        track_id := track_id() + 1
        filename = m["filename"]
        artist = m["artist"]
        title = m["title"]
        current := [("filename", filename), ("artist", artist), ("title", title)]
        cover := if m["coverart"] != "" or filename == "" then [] else find_cover(filename) end
        resend := 5 # the next tick sends the new cover
        log("track #{track_id()}: #{artist} - #{title} (#{filename})")
      end)

      # --- voice (R4) --------------------------------------------------------------

      # Spoken breaks rendered by the director into /spool/voice. Music drops to about a
      # quarter of its level (-12 dB) while a break plays, and comes back smoothly.
      voice = request.queue(id="voice")
      mixed = smooth_add(duration=0.5, p=0.25, normal=on_air, special=voice)

      # Safety net against clipping from tracks of very different level.
      radio = limit(mixed)

      # Metadata only enters the chain at a track start, so a listener who joins later
      # gets no title. Re-send the current one every 10 s, after the mix so a voice
      # break cannot drop it. The cover is heavier, so it goes out every sixth time (1 min).
      radio = insert_metadata(radio)
      thread.run(every=10., fun () -> begin
        artist = list.assoc(default="", "artist", current())
        title = list.assoc(default="", "title", current())
        resend := (resend() + 1) mod 6
        if artist != "" and title != "" then
          extra = if resend() == 0 then cover() else [] end
          radio.insert_metadata(list.append([("artist", artist), ("title", title)], extra))
        end
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
          track_id = track_id(),
          queued = list.length(music.queue()),
          current = {
            filename = list.assoc(default="", "filename", current()),
            artist = list.assoc(default="", "artist", current()),
            title = list.assoc(default="", "title", current())
          }
        })
      end)

      # POST /control/push[?liq_cross_duration=6.5&...]   body: an absolute path (raw text, no quoting)
      # Query keys starting with "liq_" become request metadata (crossfade length, fades).
      harbor.http.register(port=control_port, method="POST", "/control/push", fun (req, response) -> begin
        file_path = string.trim(req.body())
        if file_path == "" or string.sub(file_path, start=0, length=1) != "/" then
          reply_error(response, 400, "body must be an absolute path")
        elsif not file.exists(file_path) then
          reply_error(response, 404, "no such file: #{file_path}")
        else
          meta = list.filter(fun (kv) -> string.sub(fst(kv), start=0, length=4) == "liq_", req.query)
          pairs = list.map(fun (kv) -> "#{fst(kv)}=\"#{string.escape(snd(kv))}\"", meta)
          joined = string.concat(separator=",", pairs)
          uri = if pairs == [] then file_path else "annotate:#{joined}:#{file_path}" end
          music.push(request.create(uri))
          response.json({pushed = file_path, queued = list.length(music.queue())})
        end
      end)

      # POST /control/voice   body: an absolute path to a WAV; it plays over the music at once.
      harbor.http.register(port=control_port, method="POST", "/control/voice", fun (req, response) -> begin
        file_path = string.trim(req.body())
        if file_path == "" or string.sub(file_path, start=0, length=1) != "/" then
          reply_error(response, 400, "body must be an absolute path")
        elsif not file.exists(file_path) then
          reply_error(response, 404, "no such file: #{file_path}")
        else
          voice.push(request.create(file_path))
          response.json({pushed = file_path})
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
        voice.set_queue([])
        response.json({queued = 0})
      end)

      # --- output ------------------------------------------------------------------

      output.harbor(id="stream", %mp3(bitrate=192, id3v2=true), mount="live.mp3", port=8000, radio)
