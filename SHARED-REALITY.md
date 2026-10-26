# Shared VLESS/REALITY listeners

Based on upstream S-UI v1.6.6 and sing-box v1.14.4. This change shares the TCP
listener between raw TCP VLESS/REALITY inbounds using the same listen address and
port. Each logical inbound retains its tag, VLESS users, UUIDs, multiplexing,
sessions, routing rules, and traffic attribution.

## Stable selectors

Each persisted VLESS/REALITY inbound automatically receives a 16-character
hexadecimal REALITY short ID derived from its existing SQLite AUTOINCREMENT ID.
No extra column, table, or stored mapping is introduced. The selector:

- stays the same across restarts, renames, port changes, TLS template edits, and outbound changes;
- differs for each inbound record on that server;
- changes when the record is deleted and recreated;
- is generated consistently for server configuration, VLESS links, JSON, and Clash subscriptions.

These per-inbound selectors take precedence over the shared TLS template's
`short_id` list. Editing that template list does not rotate the line selectors.

When the core starts, existing generated `out_json` and local VLESS links are
updated if their selectors differ. Only the short ID is changed: addresses,
ports, UUIDs, names, external links, routes, and quotas are preserved. Repeated
starts leave matching projections untouched. A restored database preserves its
IDs and therefore its selectors.

The short ID selects a logical inbound; the selected inbound then performs its
original VLESS user authentication. Changing a selector cannot authorize a user
on a line to which that UUID is not assigned.

## Sharing requirements

Use one SNI and identical REALITY/TLS and socket settings across every inbound
sharing an address and port. Using one common TLS template is the simplest way
to maintain this. A conflicting edit is rejected before live inbounds are removed.
Editing a common TLS template validates the final batch, then recreates the
affected logical inbounds together.

Different user lists, outbound routing, and per-inbound multiplexing options are
allowed. Removing a logical inbound leaves its peers listening; removing the
last one releases the socket.

The shared path currently applies to **VLESS + REALITY over raw TCP**. Other
protocols, ordinary TLS/plain VLESS, and WebSocket/gRPC transports retain their
upstream behavior and require non-conflicting sockets. Other protocols remain
available in the panel. No external TCP proxy or client-program change is needed.

## Build and test

The pinned uTLS dependency parses the authenticated short ID but does not expose
it. `patches/utls-reality-short-id.patch` adds a read-only accessor after a successful
handshake, without changing the REALITY wire protocol or existing Conn field offsets.

Use the wrapper for this fork instead of invoking `go build/test/vet` directly:

```sh
. ./build-tags.sh
sh scripts/relay-go.sh test -ldflags="$(ldflags_for test)" -tags "$(tags_for test)" ./...
sh scripts/relay-go.sh vet ./...
sh scripts/relay-go.sh build -ldflags="$(ldflags_for test)" -tags "$(tags_for test)" -o sui main.go
```

The wrapper copies the pinned module into `.cache/`, applies the patch there, and
uses a temporary modfile. The shared module cache and tracked go.mod/go.sum remain
unchanged. It requires Go from go.mod, Git, and the upstream C compiler prerequisites.
Build frontend assets using upstream instructions before producing a deployable binary.
The existing Linux, Docker, and release workflow entrypoints use this wrapper.
When updating the pinned uTLS version, regenerate and test the accessor patch
against that version before building.

## Deployment and rollback

1. Preserve the original binary and a consistent full SQLite backup, including all IDs.
2. Confirm every intended shared inbound uses raw TCP VLESS/REALITY, one SNI, and
   matching TLS/listen settings. Keep existing tag-to-outbound rules unchanged.
   Client-side address overrides must use that same SNI as well.
3. Install the tested binary and let startup update the generated selectors.
   Changing existing public ports to 443 is a separate, explicit configuration edit;
   the binary does not rewrite ports or route rules automatically.
4. Regenerate/refresh subscriptions once for the initial new port/selector values.
   Later restarts and ordinary inbound edits do not rotate selectors.
5. Verify one physical listening socket, correct exits for multiple lines of the
   same user, unauthorized-line rejection, TCP/UDP, traffic accounting, hot edits,
   and another service restart against the persisted database.

Future upgrades must use a tested build of this fork. The upstream updater
downloads the upstream binary and would replace the shared-listener implementation.

To roll back, restore the original binary and the matching pre-change database
together. Upstream cannot start several independent listeners on the same socket,
and its template selectors do not match this fork's generated selectors. Keep the
pre-change configuration and avoid deleting/recreating inbound records as a rollback.
