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

- **Correction, 30 August 2026:** an earlier reading of this — that the
  orchestrator and authorizer "never retry" after losing a startup race with the
  ESR — was wrong. `registration.go` re-resolves the registrar every five
  seconds, and `CachedURL.Resolve` deliberately does not cache a failure, so both
  recover on their own. The `failed to find lead registrar` line is one transient
  failure being logged, not a permanent state. What actually broke that cloud was
  configuration: an empty authorizer URL and a plaintext orchestrator URL.

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

- **meteorologue reports success while serving frozen readings.** Confirmed on
  the cottage 25 August 2026: every temperature flatlined at 13:58 and stayed
  flat for hours while the pane logged `Netatmo: data refreshed` every five
  minutes and the Netatmo phone app showed live values. Two defects compound in
  `meteorologue/thing.go`:

  **Cause confirmed 25 August, from the timing.** The cloud started at 10:58 and
  `newTokenManager` refreshes on startup, so the access token was minted then.
  Netatmo access tokens live 10800 s. The readings froze at 13:58 — three hours
  to the minute. The token expired exactly on schedule and the expiry was never
  noticed, because the pane never once printed `Netatmo: access token expired,
  refreshing...`: Netatmo signals an expired token with **403** and
  `{"error":{"code":3,...}}`, not 401. (The status is not logged, so it cannot be
  read off directly — that is part of the defect.)

  1. `getWithAutoRefresh` handles **only** status 401. Any other non-200 — 403,
     429, 5xx — is returned as a body with a nil error. A Netatmo error envelope
     (`{"error":{"code":26,...}}`) then unmarshals *successfully* into
     `StationsDataResponse`, because Go ignores unknown fields, leaving
     `Body.Devices` empty. `pollNetatmo` ranges over nothing, updates no cache
     entry, and falls through to `log.Println("Netatmo: data refreshed")`. The
     same bug class as the kgrapher/modeler error-body parse, but louder: the
     system actively asserts success. Check the status, and treat an empty
     device list as an error rather than a quiet no-op.

  2. **Staleness is measured and then discarded.** `DashboardData.TimeUTC` is
     parsed and `CachedMeasurement.Timestamp` is stored, and nothing ever reads
     either. A reading older than a few poll periods should not be served as
     current — the consumer cannot tell, and ethermostat's frost guard fires on
     *absent* readings, not stale ones.

  **Refresh-token rotation is the trap waiting at the fix.** Netatmo rotates and
  invalidates *both* tokens on every refresh. `postToken` does persist the new
  pair, so the happy path is correct, but three edges are sharp: (a)
  `newTokenManager` refreshes on every startup, so any second process against the
  same `tokens.json` — a stray instance, a copy rsynced to another Pi, a restart
  racing a shutdown — leaves one holding an invalidated token and kills the grant
  permanently; (b) a failed `saveTokenFile` is only a warning, leaving a live
  token in memory and a dead one on disk; (c) the recovery path is
  `authorizeWithBrowser`, which wants a browser on a headless Pi. Serialize the
  refresh under the mutex with a double-check, and fail the refresh when the file
  write fails.

  Also: **`tokens.json` is not in `.gitignore`** though it is a bearer
  credential. Nothing has leaked — no copy exists in the working tree — but it is
  one `git add -A` from GitHub.

  Safe only by luck on 25 August: the frozen values sat below setpoint, so
  ethermostat held the plugs on and the radiators' own thermostats governed. Had
  they frozen above setpoint the plugs would have been held off indefinitely with
  no system reporting a fault. See the philosopher note in
  `chronicler/README.md` — *this signal has not changed since Tuesday* is the
  check that catches all of this from outside.

- **maitreD is Linux-only, and does not say so.** Attestation resolves a PID to
  an executable by reading `/proc/<pid>/exe` (`maitreD/hostload.go`,
  `resolveExecutable`). That is the *entire* platform dependency — the CA passes
  the PID in `X-Process-PID` and picks which maitreD to ask from the CSR's
  `RemoteAddr`, so there is no socket-to-process mapping to port, which is the
  part that would have been hard.

  **The trap is that it compiles everywhere and then lies.** `GOOS=darwin` and
  `GOOS=windows` both build today, because `os.Readlink` is portable; it simply
  fails at runtime with ENOENT, and `describeResolutionFailure` maps that to
  *"no process %d: it exited before it could be attested"*. An operator on a Mac
  would be told the process vanished, not that the platform is unsupported. Fix
  that message first, whatever else happens.

  The port itself follows the convention `busdriver` and `sailor` already use
  (`can_linux.go`): split into `hostload_linux.go` / `_darwin.go` / `_windows.go`.
  - **darwin**: `proc_pidpath()` from `<libproc.h>`, needs cgo, unprivileged for
    same-UID processes. Returns a *path*, so it hashes the file rather than the
    running inode — weaker than Linux. `SecCodeCheckValidity` is the native repair.
  - **windows**: `QueryFullProcessImageName` via `golang.org/x/sys/windows` — no
    cgo needed, and *stronger* than Linux in one respect, since the loader locks
    a running image so its bytes cannot change under the hash.

  Three things make a Mac or Windows host awkward regardless of the code:
  whitelist churn (a dev machine's binaries are build outputs, not deployed
  artifacts, so every `go build` needs re-registering); reachability (the CA
  connects *inbound* to `maitreDHosts`, and a laptop's IP changes per network —
  see the maitreDHosts item above); and the self-declared PID (a process must be
  on the CSR's host but may name a whitelisted sibling's PID — a narrow surface
  on a dedicated Pi, a wide one on a general-purpose machine). The CA's attest
  call is also plain HTTP (`ca/thing.go`).

  **Decided 25 August 2026:** an admin host gets its own subject in
  `policies.json` with read-only rights — no actuation. A weaker attestation
  domain should be *authorized less*, not refused; that is what ABAC is for. The
  governing principle is not macOS-versus-Linux but **a controller must be
  present when nobody is, and reachable at the same address tomorrow** — which a
  laptop is not, and a Mac mini on a fixed address would be. Note also that the
  Mac already holds the most consequential position in the system as the *build
  machine*: the whitelist attests that a binary is the one that was built, never
  that it was built correctly.

  Build the darwin port when there is a user for it (there is: `envoy` on the
  laptop). Draw the boundary so Windows is later a file rather than a refactor,
  and build it when a real Windows host appears — plausible in a plant, where
  engineering workstations and Cadmatic are Windows, but nobody is asking today.

- **DONE 30 August 2026: the operator can see the cloud.** `envoy -serve` proxies
  the painter's canvas on 127.0.0.1, the canvas panel opens on a click and links
  to each system's `/doc`, and the whole thing runs from `systems.txt` like any
  other system. What that took, beyond the proxy itself, is worth remembering:
  a one-shot tool promoted to a resident system needed resident behaviour in
  three separate places — it waited 90 s for a certificate and exited, it treated
  "nothing discovered yet" as fatal, and it had never met a token expiry. Each
  failed only on a restart, which is exactly when somebody opens the viewer.

  Still open from the original entry: `/doc` itself. See the entry below.

- **A person cannot see the painter's canvas, and the fix is a delegating envoy.**
  `view` and `model` are unit-asset services, so they go through `permitted()`
  and want a token; on HTTPS the listener is `RequireAndVerifyClientCert`, so a
  browser fails the handshake outright. The check therefore guards a door only
  the wrong caller can open — nothing machine-side consumes the canvas, only a
  human does.

  **Decided 25 August 2026:** give `envoy` a proxy mode rather than giving the
  painter a login. Delegation is the pattern already established, it changes no
  invariant — systems hold certificates, people do not — and it keeps human
  identity, sessions and credential storage out of the framework entirely. The
  painter-with-a-login alternative was considered and set aside: it works from
  any browser anywhere, but it makes the painter a confused deputy and puts
  password handling into a system whose job is drawing.

  It should be simpler than proxying usually is: the canvas fetches with a
  **relative** URL (`fetch("model", …)` in `painter/page.go`), so serving it under
  the same path on localhost needs no URL rewriting.

  Constraints for whoever builds it:
  - **Bind 127.0.0.1 only.** Binding the LAN would turn one delegated credential
    into an open gateway for anyone on the network — the boundary warning from
    `chronicler/README.md`, one level down.
  - **GET only.** envoy already searches with `Search4MultipleServicesAs(…, "read")`
    and registers no services; a proxy is tempting to make general, and must not be.
  - **Guard against DNS rebinding.** A page in the operator's browser can be made
    to issue requests to 127.0.0.1. Check `Host`/`Origin`, or require a secret in
    the path.
  - **A mode flag, not a second binary** — the whitelist keys on the hash, so one
    binary means one entry to maintain.
  - It becomes **long-lived**, unlike the one-shot capture, so it is the first
    consumer to exercise proactive token renewal in anger. That comes free from
    the `consumption.go` renewal work, but it is now load-bearing rather than
    theoretical.

  Runs on the Pi today; runs on the laptop once the darwin maitreD exists, which
  is the reason to do that port. **beehive is deliberately excluded** — it
  actuates, and a browser that can switch a heater with nothing attested behind
  it is a different decision that has not been taken.

- **`/doc` is still anonymous on every plaintext port, and now load-bearing.**
  Trimmed on 30 August so it names only the address the request arrived on
  rather than enumerating every address of the host — a Pi running Docker was
  publishing `172.17.0.1`, `172.18.0.1` and `172.19.0.1` to anyone on the LAN,
  which told a reader nothing they could use and an attacker that the host runs
  containers.

  What it still hands out: every system's name and description, each asset's
  `FunctionalLocation`, every service with its forms, units and **methods** — so
  which services accept a PUT — and the bound ports. The data itself is safe: a
  value over plaintext answers 401. This is a map of the plant marked with what
  can be written to, not access to it.

  **Do not close it before the replacement exists.** Over https it already sits
  behind `RequireAndVerifyClientCert`, so a browser cannot read it there at all —
  plaintext is the only way a person sees it. Closing it now would take away the
  only view of a system that has *not yet joined the cloud*, which is precisely
  when a deployer needs one, and the canvas cannot help there because the canvas
  is built from the graph.

  The order is: (1) teach the envoy proxy to serve `/doc` — it already holds a
  `core`/`syslist` policy, so it can enumerate; (2) make `docs.go` emit
  **relative** links, because it builds them absolutely from `IPAddresses[0]` and
  a proxied page would send every click back to the plaintext port — a fix worth
  making anyway, since that URL is already wrong behind NAT or on a multi-homed
  host; (3) then replace the plaintext page with a minimal self-status: identity,
  enrollment state, what it is retrying. Not the cloud's structure.

- **The maitreD serves a stale whitelist for up to five minutes after a redeploy.**
  It loads `whitelist.cache.json` at startup and re-syncs from the CA every five
  minutes, so every binary rebuilt in between fails attestation with *"Executable
  not in whitelist"* — three times on 30 August, each time looking like a fresh
  fault. It recovers on its own and the cache is what makes it survive a CA
  outage, so this is a trade rather than a defect; what is missing is that
  nothing says "the list I am judging against is N minutes old".

- **System-level mobility is not derived.** Agreed 30 August: an asset declares
  what constrains it, and a system's mobility is the most restrictive of its
  assets — fixed if any is fixed, tethered if any is tethered (union of tethers),
  movable only if all are. The asset half is done; the system half is not, so
  anything reading the graph has to fold over the assets and get the rule right
  independently. It should never be declarable on the system: a hand-written
  value can contradict its assets, and it goes stale silently the first time an
  asset with a GPIO pin is added to a system marked movable.

## Not yet run on hardware

The mission type, `ServicePointList_v1`, the cervice lock, the client transport
and the whole subscription mechanism are committed and tested, and only the last
has been near a Raspberry Pi. The staged-upgrade path for the discovery form —
an old orchestrator with a new consumer — is covered by unit tests and has never
been run as a mixed cloud.
