# To do

Things found and not yet done, so they are not carried in somebody's head. A
problem diagnosed in one afternoon and left for another is easily lost when the
work branches, which is what this file is for.

Add to it when something is found and not fixed in the same breath. Take things
off it by doing them, not by deciding they no longer matter — if they do not,
say why here and then delete the entry.

## Now redundant, to be removed

- **The ESR's hand-rolled `syslist` backfill** (`esr/thing.go`, in `newResource`).
  The framework fills a configured asset's services from its template now
  (`usecases.fillServicesFromTemplates`), which covers a service the file has
  never heard of — exactly this case. The special case was left in place rather
  than removed on the same day the general fix landed. Remove it, and let the
  ESR's own test for a declared `syslist` prove the general path works.

## Unexplained

- **`tls: bad record MAC` on a subscription stream.** Seen twice in three
  minutes on the testbed, 18 August, on the thermostat following the ds18b20's
  temperature over HTTPS:

      following temperature ended (local error: tls: bad record MAC)

  The fallback did its job — it polled, then resumed — so nothing stopped, and
  that is exactly why it is worth chasing before it is relied upon. A bad record
  MAC is a TLS integrity failure: the receiver could not authenticate a record.
  On a wired LAN that is not ordinary corruption, and the usual software cause
  is two goroutines writing one TLS connection. Nothing in the publisher or the
  follower obviously does that, so it is not diagnosed.

  Worth trying: whether it happens over plain HTTP as well (which would rule TLS
  out), whether it correlates with the sensor's empty reads, and whether a
  second consumer following the same service makes it more frequent.

## Security

- **`/kgraph` and `/smodel` are governed by nothing.** They are served by
  `handleThreeParts`, which never calls `permitted`, so every system's ontology
  is readable over plain HTTP by anything that can reach the port. Closing it
  properly means registering them as services so a token can be minted; the
  cheaper half is to refuse callers with no certificate. Discussed and set
  aside as too complicated for one sitting — the exposure is still open.
- **`nurse` names a public triple store.** `nurse/thing.go` has
  `GraphDB_URL: "http://13.79.36.131:7200/repositories/arrowhead-skoghall-v2"`
  as a template default: a public address, plain HTTP, no credentials.
- **The kgrapher sends no credentials to GraphDB** and has no configuration
  field for any. GraphDB supports users and tokens; this deployment uses
  neither.

## Correctness

- **The systems README calls the thermostat a PID controller.** Its code has
  only `Kp` and `Ki`.
- **`thermostat`'s `controller_2` asks for a temperature and a rotation that
  nothing provides**, on the testbed. Every control cycle logs a failure for it.
  Either give it providers or remove it from the configuration.
- **`kgrapher` and `painter` declare no `localcloud`.** Beyond the painter's
  title, this is why the kgrapher once refused to assemble: it needs at least
  one system to say which cloud it is in.

- **`orchestrator` serves `squests` but never registers it.** `serving`
  dispatches the path and `orchestrateMultiple` answers it, and no
  `components.Service` declares it, so it is absent from the registry, from the
  graph and from every AAS built out of them. A consumer can only find it by
  reading the Go.

- **The Asset Interfaces Description has not been read by AAS tooling.** It is
  built against the published IDTA 02017-1-0 template and unit-tested against
  it, and nothing has yet loaded it into the AASX Package Explorer or FA³ST to
  confirm the shape is accepted rather than merely plausible.

- **A system that loses its `systemconfig.json` silently stops enforcing.**
  Found on AlphaCloud: ds18b20 was running correctly from a configuration whose
  file had been deleted. A restart would have regenerated a template — and a
  generated template carries the authorizer slot empty, which means absent, so
  the system would have come back up serving without checking a token and said
  nothing about it. The reconstructed file is in place, but the hazard is
  general and it fails open. A provider holding a CA-signed certificate has
  evidence it once belonged to a cloud that had a CA; coming up with no
  authorizer configured is at least worth a line in the log, and possibly worth
  refusing until an operator says otherwise.

- **A permission cannot be withdrawn from a system that is using it.** Removing
  a rule from `policies.json` takes effect on the next decision, but a consumer
  holding a token keeps working until it expires — five minutes for an actuation
  on AlphaCloud. That is the same mechanism that lets a cell survive the
  authorizer being down, so it is a trade rather than a defect, but there is no
  way at present to say "stop now". A revocation list checked at the provider
  would be the obvious answer and is not written.

## Not yet run on hardware

The mission type, `ServicePointList_v1`, the cervice lock, the client transport
and the whole subscription mechanism are committed and tested, and only the last
has been near a Raspberry Pi. The staged-upgrade path for the discovery form —
an old orchestrator with a new consumer — is covered by unit tests and has never
been run as a mixed cloud.
