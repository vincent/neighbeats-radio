# R5: Feedback into taste

Goal: what I play and skip on the radio counts, the same way Jellyfin plays and skips do.

Today neighbeats learns taste from Jellyfin (plays, favourites, Playback Reporting skips).
Radio plays never pass through Jellyfin, so the station would keep proposing tracks I skip.

## Options

| Option | How | For | Against |
|--------|-----|-----|---------|
| A. Report to Jellyfin | director reports playback start/stop for each track as a Jellyfin client session, with the item id | taste stays in one place; plays also show in Jellyfin; nothing new in neighbeats | needs item ids (known after `sync-jellyfin`); Jellyfin's session reporting API must be read live, not assumed; skip detection then depends on Playback Reporting's rules |
| B. `POST /v1/feedback` in neighbeats | director sends `{item, event: play\|skip, at}`; neighbeats merges it with the Jellyfin history | exact skip semantics, works without item ids | new table and merge logic in neighbeats |
| C. Scrobble | ListenBrainz or Last.fm from the director | nice to have anyway | neighbeats does not read it |

Default: **try A first** (one probe against the live Jellyfin API), fall back to B if the
reported plays or skips do not show up in what `sync-jellyfin` reads. C is independent (later).

## Tasks

- [ ] Probe: report one play from a script; run `sync-jellyfin`; check it is counted.
- [ ] Implement the chosen option; skips from `neighbeats-radio skip` and from track ends shorter
      than N seconds (if the player ever gets a skip button).
- [ ] Taste user = config `user`, passed on every `/v1/path` and `/v1/pick`.

## Exit criteria

- A track skipped twice on the radio stops being proposed (as `comfort` already does for
  Jellyfin skips).
