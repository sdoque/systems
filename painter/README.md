# Painter

The painter shows an operator what the local cloud is doing. It serves one page:
the cloud, the hosts in it, the systems on each host, the unit assets in each
system, and lines for the services they consume from one another. Roll the mouse
wheel to go from the whole cloud down to a single service and back out.

## Problem and solution

A local cloud is a system of systems, and nothing in it holds the whole picture.
Each system knows what it provides, what it consumes and how secure it believes
itself to be; no system knows the shape of the cloud around it. An operator
standing in front of a plant has the same problem, and answers it today by
reading a registry listing of several dozen service records — which says what
exists and not what it looks like.

The painter asks every system to describe itself, puts the answers side by side,
and draws them.

## Not the KGrapher

The [kgrapher](../kgrapher/README.md) reads exactly the same thing — each
system's `/kgraph` — and does entirely different work with it. It assembles a
knowledge graph, uploads it to a triple store, keeps snapshots and lets anyone
ask questions of the result.

The painter keeps nothing. There is no triple store, no SPARQL, no reasoner and
no history: the picture is the cloud as it is now, and when the cloud changes the
picture changes. If you want to ask a question, use the KGrapher. If you want to
see what is happening, look here.

Two systems rather than one, because the two jobs pull in opposite directions. A
knowledge graph must be correct, complete and durable, and pays for that in
machinery. A picture must be immediate and legible, and can be a few seconds out
of date without misleading anybody.

## What the picture shows

**Nested disks.** The cloud is a disk; hosts are disks on it; systems are disks
on their host; unit assets are dots within their system. Depth is suggested by
shade and scale rather than drawn in perspective, because the point is to be
understood quickly rather than to be impressive.

**Colour is security, and only security.** A system is coloured by the posture it
reports about itself — `authorized`, `identified`, `enrolling`, `open`. One
system in the corner that never finished enrolling is visible from across a room.
Because colour carries that meaning everywhere, nothing else uses it.

**Lines are consumption.** A line runs from an asset that consumes a service to
the asset that provides it, found by matching the URL the consumer was bound to
against the URL the provider published. Provided services that nobody consumes
are not drawn: they are not yet part of how the cloud works.

**Line style is mission.** Solid observes, dashed acts. Driving a heater should
not look like reading a thermometer.

**A pulsing dot means a service nothing provides.** An asset that asked for a
service and found no provider is the state that looks healthy from every other
angle — every system running, every light green, and one control loop with no
input. It is the reason the painter shows what is *missing* and not only what is
there.

**Things keep their places.** A system's position is derived from its name, not
from a layout run, so it lands in the same spot in every browser and returns to
that spot after a restart. Systems fade in when they arrive and dim before they
disappear, because "gone" and "not answering just now" are different things.

## Zoom

The wheel changes what is worth showing, not the magnification:

| Scale | What appears |
|-------|--------------|
| out   | the cloud and its hosts; lines bundled into one strand per pair |
| →     | the systems on each host, coloured by posture |
| →     | the unit assets within each system, and unsatisfied requests |
| in    | services, with lines separated and labelled |

## Services

| Service | Sub-path | Methods | Description |
|---------|----------|---------|-------------|
| `view` | `view` | GET | the page itself |
| `cloudpicture` | `model` | GET | the same picture as JSON, which the page redraws from |

The page is self-contained: no stylesheet, script or font is fetched from
anywhere. A plant's network reaches the machines in it and often nothing else,
and a page that needs a library from a content delivery network is a page that
works at a desk and shows a blank screen in a substation.

## Checking the drawing

The page is the half of this system Go cannot test. macOS ships a JavaScript
engine, so the drawing can be executed and asked what it produced:

```bash
cd painter && osascript -l JavaScript page_check.js
```

It reads the script out of `page.go` rather than copying it, so the two cannot
drift. It is not a browser — no layout, no paint, no SMIL — so it proves the code
runs and emits the right elements, not that the result looks right. That
distinction is worth keeping in mind: it has caught faults a Go test could not
see, and it would not have caught the one where every line was drawn underneath
an opaque disk.

## Configuration (`systemconfig.json`)

```json
{
  "systemname": "painter",
  "unit_assets": [
    {
      "name": "canvas",
      "mission": "aggregation",
      "traits": [ { "samplingPeriod": 15 } ]
    }
  ],
  "protocolsNports": { "coap": 0, "http": 20107, "https": 30107 }
}
```

`samplingPeriod` is how often, in seconds, the painter walks the cloud again. It
polls rather than subscribing: the picture is redrawn because somebody is
watching, not because the cloud changed, and polling means the painter works in a
cloud whose registrar offers no subscription.

## Example

```bash
# the page
open http://localhost:20107/painter/canvas/view

# what it draws from
curl -s http://localhost:20107/painter/canvas/model | jq .
```

## Dependencies

| System | Role |
|--------|------|
| `esr` / `serviceregistrar` | lists the systems to ask |
| `orchestrator` | supplies the token for reading that list in an authorized cloud |

Every other system is a source: the painter reads each one's `/kgraph`. A system
that will not answer is noted on the page rather than omitted from it.

## Contributors

- Jan A. van Deventer, Luleå — initial implementation

## License

MIT — see [LICENSE](../../LICENSE) for details.
