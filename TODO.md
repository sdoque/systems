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

- **A fresh install cannot start at all.** Two defects compound, and together
  they mean a cloud this framework generates today, on one host, never comes up.
  Reproduced from nothing on a second Pi, 22 August.

  First: `usecases.setupDefaultConfig` seeds every core URL with the host's LAN
  address, so a maitreD reaching the CA arrives from that address, while the
  CA's own template authorizes maitreD enrollment from `127.0.0.1` and `::1`
  only. Two defaults written by the same framework disagree:

      certify: denied maitreD enrollment from "10.0.0.33" (maitreDHosts=[127.0.0.1 ::1])

  Second, and independent: the maitreD fetches the whitelist **once** at
  startup. `runSyncLoop` tries, and on failure with no cache on disk returns an
  error that exits the process. So the refusal above is not survivable — and
  neither is losing the startup race with the CA, which is what happens next
  once the host list is corrected:

      whitelist bootstrap failed: first whitelist fetch failed and no cache exists:
      Get "http://10.0.0.33:20100...": connection refused

  With no maitreD nothing can be attested, so no system gets a certificate and
  the cloud never starts. The symptom names the whitelist while the cause is
  first the host list and then a race, which is what makes it expensive to find.
  Starting the maitreD by hand once the CA was listening brought all eight
  systems up in forty seconds, which is the whole of the diagnosis.

  On a host that has run before, a stale `whitelist.cache.json` hides both: the
  maitreD continues on the cache and every system loops on "Executable not in
  whitelist" until the five-minute ticker succeeds. That is the cottage's
  version, and it costs five minutes of denied attestation after every deploy
  and every power cut, during which nothing enrolls and the heating is
  uncontrolled.

  Two fixes, and the second matters more. The CA could authorize its own host's
  addresses by default: it knows them, and a process on the CA's own machine is
  already trusted through loopback, so this grants nothing that was not granted
  already. And the maitreD's *initial* fetch should retry with a short backoff
  before settling into the five-minute cadence — one attempt against a CA
  starting in the same second is a race the framework need not lose.

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

- **A system's name is its certificate common name, and policy matching is
  exact.** `collector` was the one capitalised system, so the authorizer
  README's own worked example — `"subject": "collector"` — was a rule that could
  never have matched it. Renamed, and a test now pins that a capitalised subject
  does not match a lowercase rule. Worth a check at startup: a system whose name
  differs from its configuration's `systemname` in case alone is a policy that
  silently does nothing.

- **The Asset Interfaces Description has been read by FA³ST, not by a consumer.**
  FA³ST 1.3.0 accepts all four submodels and returns the interface description
  intact, four levels deep, so the shape is right. What that does not show is
  whether the semantics are useful: nothing has yet taken `observable`, `unit`
  and `valueSemantics` out of a shell and done something with them. The AASX
  Package Explorer has still not opened one, and its view is the one an auditor
  would use.

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
