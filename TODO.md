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

## Not yet run on hardware

The mission type, `ServicePointList_v1`, the cervice lock, the client transport
and the whole subscription mechanism are committed and tested, and only the last
has been near a Raspberry Pi. The staged-upgrade path for the discovery form —
an old orchestrator with a new consumer — is covered by unit tests and has never
been run as a mixed cloud.
