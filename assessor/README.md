# mbaigo System: assessor

**Status: implemented, not yet run against a live GraphDB.**

The assessor says what can go wrong with the local cloud and what would follow
if it did — a Failure Mode and Effects Analysis that maintains itself.

```
GET /assessor/analyst/fmea   →  the assessment as CSV
GET /assessor/analyst/scope  →  what it covers, as JSON
```

---

## Why this can be a system rather than a spreadsheet

An FMEA is normally a document. Somebody sits down with the design, lists what
each component does, imagines how each could fail, traces what depends on it,
and rates the result. The tracing is the laborious part and it is also the part
that goes stale first: add a system, and every effect column downstream of it is
quietly wrong.

But an Arrowhead cloud already knows its own structure. `afo:providesService`
says what each asset offers, `afo:consumesService` says who depends on it, and
following those edges to their leaves *is* the effects analysis. In a cottage
with one indoor sensor and two heated rooms, the fact that losing that sensor
opens the loop on both is a graph traversal, not an insight.

So the assessment splits in two, and the split is the whole design.

| | Comes from | Changes when |
|---|---|---|
| Failure modes, effects, detection gaps, evidence | the knowledge graph | the cloud changes |
| Severity, occurrence, detection ratings | `valuation.json` | the owner's judgment changes |

Neither half pretends to be the other. **This is the same division the
[authorizer](../authorizer/POLICY.md) makes** — `policies.json` holds the human
judgment and the engine derives the rest — and for the same reason: a generated
document nobody can argue with is not more trustworthy, it is less.

---

## The deterministic half

Every finding is a statement the graph entails. Where a row says a service has
no consumer, no consumer exists. Where it says a controller depends on one
sensor, there is one sensor. Each carries a **Graph evidence** column naming the
triples, so a reader can check the claim rather than trust it.

| Check | What it finds |
|---|---|
| `checkDanglingConsumption` | A dependence that resolves to nothing — a failure that has already happened |
| `checkOrphanService` | Something published and consumed by nobody: spare capacity or an omission, and the graph cannot say which |
| `checkSingleProvider` | One provider of something a controller needs |
| `checkUnboundedWritable` | A writable quantity with a unit, a quantity kind and no permitted range |
| `checkPolledDependence` | A value that is polled, so "unchanged" and "stopped arriving" look alike |
| `checkSharedSensorAcrossLocations` | One sensor driving control in two places: both loops correct, one room wrong |
| `checkLocationVocabulary` | The same room written both as an IRI and as a literal |
| `checkLocationLiteral` | A location with a trailing space, or decoded through the wrong character set |
| `checkSingleHost` | Every system on one machine, which no service redundancy survives |

Precision matters more than count. A boolean command is not reported as needing
a numeric range, and a service a consumer *writes* is not reported as serving a
stale reading — an FMEA full of rows with no action behind them is one nobody
reads. `TestWhatIsNotAFinding` holds both.

---

## The judged half: `valuation.json`

The graph cannot know how much any of this matters. A heater failing in a
Norrbotten cottage in January and the same heater failing in a summer house are
the same failure and not remotely the same consequence, and no triple
distinguishes them.

**The ratings attach to classes, not to rows.** Per-row ratings would need
re-judging every time a system was added, which is exactly what leaves FMEA
spreadsheets to rot. A class is a kind of consequence and keeps its rating while
the cloud changes shape underneath it.

```json
"severity": {
    "loss-of-input": {
        "rating": 7,
        "means": "A controller runs open-loop. It holds its last output while the room does as it pleases."
    }
}
```

The prose is not decoration. An FMEA is read by people deciding where to spend,
and "7" persuades nobody; the sentence is the argument.

Three sets of classes are rated — see [valuation.example.json](valuation.example.json):

- **severity**, on the end effect: `total-loss`, `loss-of-control`,
  `loss-of-input`, `silent-wrong-control`, `wrong-by-design`,
  `silent-rule-failure`, `unused-information`
- **occurrence**, on the cause: `model-omission`, `model-inconsistency`,
  `configuration-typo`, `architectural-choice`, `device-unavailable`,
  `upstream-stalled`, `host-failure`
- **detection**, on how it would be noticed: `unobservable-from-within`,
  `no-constraint`, `no-staleness-signal`, `published-not-consumed`,
  `no-validation`, `graph-only`, `heartbeat`

Note what falls out of this. The model-defect classes rate **occurrence 10 by
definition** — a missing binding is not a risk, it is already the case — so they
rank above hardware failures that are merely likely. That is usually the right
answer and it is worth understanding rather than being surprised by.

A class a run needs and the file does not rate produces an **unscored row**, not
a zero. Unscored rows sort to the *top* of the CSV, because a finding whose
consequence nobody has valued is the one most worth putting in front of the
owner, and the file lists them at the end under *Unrated classes*.

The file is re-read when its modification time changes, so a revised judgment
shows in the next assessment rather than the next deployment. A file that stops
parsing leaves the previous ratings in place: an FMEA with every rating blank
looks like an assessment that found nothing, rather than one that failed to
load.

---

## Output

CSV, because the audience for an FMEA opens it in a spreadsheet and the audience
for a diff opens it in a text editor. A generated artifact that cannot be diffed
is one nobody reviews — the useful question about this month's assessment is not
what it says but what changed since last month's, and `diff` answers that.

Rows are ordered by RPN descending, with the unscored first. The scales are
quoted into the footer, because a severity of 8 against an automotive scale and
against a cottage scale are different claims, and a rating without its scale is
unreadable.

`Assess` is deterministic: same graph, same rows, same IDs. Without that, a diff
shows map iteration order rather than what changed in the cloud.

---

## Why GraphDB and not `/kgraph`

The [painter](../painter/README.md) reads each system's `/kgraph` directly,
because a picture must be immediate. The assessor reads the triple store,
because an analysis of what can go wrong should see everything that is known —
the reasoner's entailments, and, where the plant design and lifecycle views have
been loaded through the IDO bridges, the P&ID tag and serial number that turn "a
sensor" into a serial-numbered device with a maintenance history. That is also
where real occurrence data would eventually come from: how often *this* model of
sensor has actually failed, rather than an estimate.

See the [democrat README](../democrat/README.md) for the GraphDB setup; the
assessor reads the same `urn:state:current` named graph that kgrapher maintains.

---

## Configuration

| Field | Description |
|---|---|
| `traits[0].graphdbUrl` | SPARQL SELECT endpoint, e.g. `http://localhost:7200/repositories/Arrowhead` |

Copy `valuation.example.json` to `valuation.json` and edit it. The system
refuses to start without one, deliberately: the graph supplies the failure modes
and their effects, that file supplies how much they matter, and an assessment
with every row unscored is not an assessment.

---

## Running the tests

```bash
go test ./...
```

The fixture is a cloud *shaped* like the cottage — one indoor module feeding two
rooms, a heater whose power nobody watches, an unbounded setpoint, a binding to
an outdoor temperature nothing provides — rather than the real `cottage.ttl`.
That file describes somebody's house, and a public test fixture is a poor place
for it.

| Test | What it checks |
|---|---|
| `TestOneSensorLostOpensTwoLoops` | The traversal a spreadsheet does by hand |
| `TestADanglingDependenceIsFound` | A binding to nothing, classed as already-the-case |
| `TestAServiceNobodyConsumesIsFound` | Published and unwatched |
| `TestAWritableServiceWithNoRangeIsFound` | And that a bounded one is not reported |
| `TestASharedSensorAcrossRoomsIsFound` | One source, two rooms |
| `TestAnUntrimmedLocationIsFound` | A literal that reads correctly and compares unequal |
| `TestASingleHostIsFound` | And that two hosts are not reported |
| `TestWhatIsNotAFinding` | The two false positives that were removed |
| `TestTheSameCloudAssessesIdentically` | Stable rows and IDs, so diffs mean something |
| `TestEveryFindingNamesItsClasses` | Nothing can be emitted that cannot be scored or checked |

---

## What remains

- **Never run against a live GraphDB.** The SPARQL is written against the
  vocabulary today's kgrapher emits — `alc:hasMethods`, `alc:hasRange`,
  `alc:hasMode`, `afo:isSubscribable` — and verified against a captured graph,
  but no store has answered it.
- **The security posture is queried and not yet used.** `namesAuthorizer`,
  `verifiesTokens` and `acceptsPlaintext` are in the graph and belong in the
  assessment; the cottage FMEA had a row for exactly that.
- **Occurrence is an estimate.** Every rating in the example file is a judgment
  about a class, not a measured rate. The lifecycle view reached through the IDO
  bridges is where measured failure rates would come from, and nothing reads it
  yet.
- **No consumer of the assessment.** The assessor publishes and, at present,
  nothing consumes — which is precisely what `checkOrphanService` reports about
  everything else. It will find itself.
