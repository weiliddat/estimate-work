# Estimate.Work

A free tool to collaboratively estimate work effort via consensus AKA [Planning Poker or Scrum Poker](https://en.wikipedia.org/wiki/Planning_poker).

## Why?

The usual reasons: A lot of other online tools are free but ad-ridden; some tools don't work well and have sync issues; it seems like really simple software; some are unreasonably priced or complex.

## Development / Contributing / Self-host

1. [Go 1.23](https://go.dev)
2. Setup the right environment variables. You can refer to `.env.sample`.
   1. `LISTEN` (required) is the listening address of the server
   2. `DATA_FILE_PATH` (optional) is where to store/restore data
   3. `FLY_MACHINE_ID` (required) is an artifact of deploying this on fly.io and using it to identify machines for the proxy. If you don't host on fly.io this has no real effect; set it to your hostname or any arbitrary name.
3. `go run .` or `go build . && ./estimate-work`

### Other notes

1. This happens to be deployed via fly.io so it has fly.io related files.
2. Prettier is used to format the html files, therefore the node.js related files.

## Technical details

This is a real-time multiplayer planning poker done via htmx, long-polling, and update coordination via Go channels. Styling is done via pico.css because it looks good out of the box for the elements and forms required for this app. Emojis are used instead of icons.

There is no conventional concept of a user. Everything is tied to a "room". There is a userId that is conveniently stored via cookies and can be reused between rooms, so if you happen to switch rooms often you don't have to re-identify yourself.

This keeps the code relatively simple, and requests very lightweight. The initial transfer size (compressed) is dominated by pico (~11KB) and htmx (~21KB), which are served by CDNs and are likely cached on subsequent requests. The room page is ~4KB and realtime updates are often ~1KB or less.
